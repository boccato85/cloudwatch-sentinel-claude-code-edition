package main

import (
	"context"
	"crypto/md5"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

//go:embed static/icon.png
var iconPNG []byte

//go:embed static/dashboard.html
var dashboardHTML []byte

//go:embed static/dashboard.css
var dashboardCSS []byte

//go:embed static/dashboard.js
var dashboardJS []byte

const (
	USD_PER_VCPU_HOUR      = 0.04
	USD_PER_GB_HOUR        = 0.005
	MinWasteCPURequestMCpu = 5
)

type NodeInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type PodAlert struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type ClusterSummary struct {
	Nodes          []NodeInfo     `json:"nodes"`
	PodsByPhase    map[string]int `json:"podsByPhase"`
	FailedPods     []PodAlert     `json:"failedPods"`
	PendingPods    []PodAlert     `json:"pendingPods"`
	CpuAllocatable int64          `json:"cpuAllocatable"`
	CpuRequested   int64          `json:"cpuRequested"`
	MemAllocatable int64          `json:"memAllocatable"`
	MemRequested   int64          `json:"memRequested"`
	Efficiency     float64        `json:"efficiency"`
}

type HistoryPoint struct {
	Time    string  `json:"time"`
	ReqCost float64 `json:"reqCost"`
	UseCost float64 `json:"useCost"`
}

type PodStats struct {
	Name                string `json:"name"`
	Namespace           string `json:"namespace"`
	CPUUsage            int64  `json:"cpuUsage"`
	CPURequest          int64  `json:"cpuRequest"`
	CPURequestPresent   bool   `json:"cpuRequestPresent"`
	MemUsage            int64  `json:"memUsage"`
	PotentialSavingMCpu *int64 `json:"potentialSavingMCpu,omitempty"`
	Opportunity         string `json:"opportunity,omitempty"`
}

var (
	latestStats   []PodStats
	latestSummary ClusterSummary
	statsMutex    sync.Mutex
	db            *sql.DB
	dbTimeout     = 5 * time.Second
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func requireEnv(key string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		slog.Error("required environment variable not set", "var", key)
		os.Exit(1)
	}
	return value
}

func getEnvInt(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		slog.Warn("invalid integer environment variable, using fallback", "var", key, "value", value, "fallback", fallback)
		return fallback
	}
	return parsed
}

func withDBTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, dbTimeout)
}

func logSQLError(operation string, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		slog.Warn("sql context error", "operation", operation, "timeout", dbTimeout.String(), "err", err)
		return
	}
	slog.Warn("sql operation failed", "operation", operation, "err", err)
}

func ensureSchema(ctx context.Context) error {
	schema := `
	-- Raw metrics (retained for RETENTION_RAW_HOURS, default 24h)
	CREATE TABLE IF NOT EXISTS metrics (
		id SERIAL PRIMARY KEY,
		pod_name VARCHAR(255) NOT NULL,
		namespace VARCHAR(255) NOT NULL,
		container_name VARCHAR(255) NOT NULL,
		cpu_usage BIGINT NOT NULL,
		cpu_request BIGINT NOT NULL,
		mem_usage BIGINT NOT NULL,
		mem_request BIGINT NOT NULL,
		opportunity VARCHAR(50),
		recorded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_metrics_recorded_at ON metrics(recorded_at);
	CREATE INDEX IF NOT EXISTS idx_metrics_pod ON metrics(namespace, pod_name);

	-- Hourly aggregates (retained for RETENTION_HOURLY_DAYS, default 30 days)
	CREATE TABLE IF NOT EXISTS metrics_hourly (
		id SERIAL PRIMARY KEY,
		pod_name VARCHAR(255) NOT NULL,
		namespace VARCHAR(255) NOT NULL,
		hour_bucket TIMESTAMP NOT NULL,
		avg_cpu_usage BIGINT NOT NULL,
		max_cpu_usage BIGINT NOT NULL,
		avg_cpu_request BIGINT NOT NULL,
		avg_mem_usage BIGINT NOT NULL,
		max_mem_usage BIGINT NOT NULL,
		avg_mem_request BIGINT NOT NULL,
		sample_count INT NOT NULL,
		UNIQUE(namespace, pod_name, hour_bucket)
	);
	CREATE INDEX IF NOT EXISTS idx_metrics_hourly_bucket ON metrics_hourly(hour_bucket);
	CREATE INDEX IF NOT EXISTS idx_metrics_hourly_pod ON metrics_hourly(namespace, pod_name);

	-- Daily aggregates (retained for RETENTION_DAILY_DAYS, default 365 days)
	CREATE TABLE IF NOT EXISTS metrics_daily (
		id SERIAL PRIMARY KEY,
		pod_name VARCHAR(255) NOT NULL,
		namespace VARCHAR(255) NOT NULL,
		day_bucket DATE NOT NULL,
		avg_cpu_usage BIGINT NOT NULL,
		max_cpu_usage BIGINT NOT NULL,
		avg_cpu_request BIGINT NOT NULL,
		avg_mem_usage BIGINT NOT NULL,
		max_mem_usage BIGINT NOT NULL,
		avg_mem_request BIGINT NOT NULL,
		sample_count INT NOT NULL,
		UNIQUE(namespace, pod_name, day_bucket)
	);
	CREATE INDEX IF NOT EXISTS idx_metrics_daily_bucket ON metrics_daily(day_bucket);
	CREATE INDEX IF NOT EXISTS idx_metrics_daily_pod ON metrics_daily(namespace, pod_name);

	-- Cost history (retained same as daily)
	CREATE TABLE IF NOT EXISTS cost_history (
		id SERIAL PRIMARY KEY,
		recorded_at TIMESTAMP NOT NULL,
		total_cpu_cost DECIMAL(10,4) NOT NULL,
		total_mem_cost DECIMAL(10,4) NOT NULL,
		total_waste_cost DECIMAL(10,4) NOT NULL,
		pod_count INT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_cost_history_recorded_at ON cost_history(recorded_at);
	`
	_, err := db.ExecContext(ctx, schema)
	return err
}

// aggregateHourlyMetrics aggregates raw metrics older than 1 hour into hourly buckets
func aggregateHourlyMetrics(ctx context.Context) error {
	query := `
	INSERT INTO metrics_hourly (pod_name, namespace, hour_bucket, avg_cpu_usage, max_cpu_usage, avg_cpu_request, avg_mem_usage, max_mem_usage, avg_mem_request, sample_count)
	SELECT 
		pod_name,
		namespace,
		date_trunc('hour', recorded_at) as hour_bucket,
		AVG(cpu_usage)::BIGINT as avg_cpu_usage,
		MAX(cpu_usage) as max_cpu_usage,
		AVG(cpu_request)::BIGINT as avg_cpu_request,
		AVG(mem_usage)::BIGINT as avg_mem_usage,
		MAX(mem_usage) as max_mem_usage,
		AVG(mem_request)::BIGINT as avg_mem_request,
		COUNT(*) as sample_count
	FROM metrics
	WHERE recorded_at < date_trunc('hour', NOW())
	GROUP BY pod_name, namespace, date_trunc('hour', recorded_at)
	ON CONFLICT (namespace, pod_name, hour_bucket) DO UPDATE SET
		avg_cpu_usage = EXCLUDED.avg_cpu_usage,
		max_cpu_usage = EXCLUDED.max_cpu_usage,
		avg_cpu_request = EXCLUDED.avg_cpu_request,
		avg_mem_usage = EXCLUDED.avg_mem_usage,
		max_mem_usage = EXCLUDED.max_mem_usage,
		avg_mem_request = EXCLUDED.avg_mem_request,
		sample_count = EXCLUDED.sample_count
	`
	_, err := db.ExecContext(ctx, query)
	return err
}

// aggregateDailyMetrics aggregates hourly metrics older than 1 day into daily buckets
func aggregateDailyMetrics(ctx context.Context) error {
	query := `
	INSERT INTO metrics_daily (pod_name, namespace, day_bucket, avg_cpu_usage, max_cpu_usage, avg_cpu_request, avg_mem_usage, max_mem_usage, avg_mem_request, sample_count)
	SELECT 
		pod_name,
		namespace,
		date_trunc('day', hour_bucket)::DATE as day_bucket,
		AVG(avg_cpu_usage)::BIGINT as avg_cpu_usage,
		MAX(max_cpu_usage) as max_cpu_usage,
		AVG(avg_cpu_request)::BIGINT as avg_cpu_request,
		AVG(avg_mem_usage)::BIGINT as avg_mem_usage,
		MAX(max_mem_usage) as max_mem_usage,
		AVG(avg_mem_request)::BIGINT as avg_mem_request,
		SUM(sample_count) as sample_count
	FROM metrics_hourly
	WHERE hour_bucket < date_trunc('day', NOW())
	GROUP BY pod_name, namespace, date_trunc('day', hour_bucket)
	ON CONFLICT (namespace, pod_name, day_bucket) DO UPDATE SET
		avg_cpu_usage = EXCLUDED.avg_cpu_usage,
		max_cpu_usage = EXCLUDED.max_cpu_usage,
		avg_cpu_request = EXCLUDED.avg_cpu_request,
		avg_mem_usage = EXCLUDED.avg_mem_usage,
		max_mem_usage = EXCLUDED.max_mem_usage,
		avg_mem_request = EXCLUDED.avg_mem_request,
		sample_count = EXCLUDED.sample_count
	`
	_, err := db.ExecContext(ctx, query)
	return err
}

// cleanupOldMetrics removes metrics older than the configured retention periods
func cleanupOldMetrics(ctx context.Context, rawHours, hourlyDays, dailyDays int) (int64, int64, int64, error) {
	var rawDeleted, hourlyDeleted, dailyDeleted int64

	// Delete raw metrics older than retention period (keep only last hour for aggregation)
	res, err := db.ExecContext(ctx, `DELETE FROM metrics WHERE recorded_at < NOW() - INTERVAL '1 hour' * $1`, rawHours)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("cleanup raw metrics: %w", err)
	}
	rawDeleted, _ = res.RowsAffected()

	// Delete hourly aggregates older than retention period
	res, err = db.ExecContext(ctx, `DELETE FROM metrics_hourly WHERE hour_bucket < NOW() - INTERVAL '1 day' * $1`, hourlyDays)
	if err != nil {
		return rawDeleted, 0, 0, fmt.Errorf("cleanup hourly metrics: %w", err)
	}
	hourlyDeleted, _ = res.RowsAffected()

	// Delete daily aggregates older than retention period
	res, err = db.ExecContext(ctx, `DELETE FROM metrics_daily WHERE day_bucket < NOW() - INTERVAL '1 day' * $1`, dailyDays)
	if err != nil {
		return rawDeleted, hourlyDeleted, 0, fmt.Errorf("cleanup daily metrics: %w", err)
	}
	dailyDeleted, _ = res.RowsAffected()

	// Also cleanup old cost history
	_, err = db.ExecContext(ctx, `DELETE FROM cost_history WHERE recorded_at < NOW() - INTERVAL '1 day' * $1`, dailyDays)
	if err != nil {
		return rawDeleted, hourlyDeleted, dailyDeleted, fmt.Errorf("cleanup cost history: %w", err)
	}

	return rawDeleted, hourlyDeleted, dailyDeleted, nil
}

// startRetentionWorker runs aggregation and cleanup jobs periodically
func startRetentionWorker(ctx context.Context, rawHours, hourlyDays, dailyDays int) {
	// Run immediately on startup
	runRetentionJobs(rawHours, hourlyDays, dailyDays)

	// Then run every hour
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("retention worker stopped")
			return
		case <-ticker.C:
			runRetentionJobs(rawHours, hourlyDays, dailyDays)
		}
	}
}

func runRetentionJobs(rawHours, hourlyDays, dailyDays int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Aggregate hourly
	if err := aggregateHourlyMetrics(ctx); err != nil {
		slog.Warn("hourly aggregation failed", "err", err)
	} else {
		slog.Debug("hourly aggregation completed")
	}

	// Aggregate daily
	if err := aggregateDailyMetrics(ctx); err != nil {
		slog.Warn("daily aggregation failed", "err", err)
	} else {
		slog.Debug("daily aggregation completed")
	}

	// Cleanup old data
	rawDel, hourlyDel, dailyDel, err := cleanupOldMetrics(ctx, rawHours, hourlyDays, dailyDays)
	if err != nil {
		slog.Warn("cleanup failed", "err", err)
	} else if rawDel > 0 || hourlyDel > 0 || dailyDel > 0 {
		slog.Info("retention cleanup completed", "raw_deleted", rawDel, "hourly_deleted", hourlyDel, "daily_deleted", dailyDel)
	}
}

func getPodRequest(podRequestMap map[string]map[string]int64, namespace, name string) (int64, bool) {
	nsReqs, nsFound := podRequestMap[namespace]
	if !nsFound {
		return 0, false
	}
	req, reqFound := nsReqs[name]
	return req, reqFound
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

func withMiddleware(handler http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	wrapped := handler
	for i := len(middlewares) - 1; i >= 0; i-- {
		wrapped = middlewares[i](wrapped)
	}
	return wrapped
}

func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"request_id", r.Header.Get("X-Request-ID"),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()))
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("X-Frame-Options", "DENY")
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func requestLoggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strconv.FormatInt(time.Now().UnixNano(), 36)
		w.Header().Set("X-Request-ID", requestID)
		r.Header.Set("X-Request-ID", requestID)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		slog.Info("http request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "status", rec.status, "duration", time.Since(start))
	})
}

// rateLimitMiddleware creates a rate limiter that allows `rps` requests per second
// with a burst capacity of rps*2. Returns 429 Too Many Requests when exceeded.
func rateLimitMiddleware(rps int) func(http.Handler) http.Handler {
	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !limiter.Allow() {
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Retry-After", "1")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

var iconETag string

func init() {
	h := md5.Sum(iconPNG)
	iconETag = `"` + hex.EncodeToString(h[:]) + `"`
}

func main() {
	dbUser := requireEnv("DB_USER")
	dbPass := requireEnv("DB_PASSWORD")
	dbName := getEnv("DB_NAME", "sentinel_db")
	dbHost := getEnv("DB_HOST", "localhost")
	dbPort := getEnv("DB_PORT", "5432")
	// NOTE: Default "disable" is intentional for local dev (Minikube).
	// For production, set DB_SSLMODE=require or verify-full.
	sslMode := getEnv("DB_SSLMODE", "disable")
	dbTimeout = time.Duration(getEnvInt("DB_TIMEOUT_SEC", 5)) * time.Second

	// Retention settings
	retentionRawHours := getEnvInt("RETENTION_RAW_HOURS", 24)     // Raw metrics: 24h default
	retentionHourlyDays := getEnvInt("RETENTION_HOURLY_DAYS", 30) // Hourly aggregates: 30 days default
	retentionDailyDays := getEnvInt("RETENTION_DAILY_DAYS", 365)  // Daily aggregates: 1 year default

	if sslMode == "disable" {
		slog.Warn("PostgreSQL SSL is disabled — set DB_SSLMODE=require for production")
	}

	connStr := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=10",
		dbHost, dbPort, dbUser, dbPass, dbName, sslMode)

	slog.Info("connecting to PostgreSQL", "host", dbHost, "port", dbPort, "database", dbName)
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}

	// Connection pool hardening
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(1 * time.Minute)

	pingCtx, pingCancel := withDBTimeout(context.Background())
	defer pingCancel()
	if err = db.PingContext(pingCtx); err != nil {
		slog.Error("database ping failed", "err", err)
		os.Exit(1)
	}

	// Ensure database schema exists
	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err = ensureSchema(schemaCtx); err != nil {
		schemaCancel()
		slog.Error("failed to create database schema", "err", err)
		os.Exit(1)
	}
	schemaCancel()
	slog.Info("database schema verified")

	slog.Info("Sentinel Intelligence Engine: Active")
	slog.Info("retention policy", "raw_hours", retentionRawHours, "hourly_days", retentionHourlyDays, "daily_days", retentionDailyDays)

	// Try in-cluster config first (for running in Kubernetes)
	// Fall back to local kubeconfig for development
	var k8sCfg *rest.Config
	k8sCfg, err = rest.InClusterConfig()
	if err != nil {
		slog.Info("not running in cluster, trying local kubeconfig")
		home := homedir.HomeDir()
		k8sCfg, err = clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
		if err != nil {
			slog.Error("failed to load kubeconfig", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Info("using in-cluster Kubernetes config")
	}

	var clientset *kubernetes.Clientset
	clientset, err = kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		slog.Error("failed to create k8s client", "err", err)
		os.Exit(1)
	}
	metricsClient, err := metricsv.NewForConfig(k8sCfg)
	if err != nil {
		slog.Error("failed to create metrics client", "err", err)
		os.Exit(1)
	}

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// Start retention worker (aggregation + cleanup)
	go startRetentionWorker(appCtx, retentionRawHours, retentionHourlyDays, retentionDailyDays)

	go func() {
		for {
			summary := ClusterSummary{PodsByPhase: make(map[string]int)}
			ctx, cancel := context.WithTimeout(appCtx, 15*time.Second)

			nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err == nil {
				for _, n := range nodes.Items {
					summary.Nodes = append(summary.Nodes, NodeInfo{Name: n.Name, Status: "Running"})
					summary.CpuAllocatable += n.Status.Allocatable.Cpu().MilliValue()
					summary.MemAllocatable += n.Status.Allocatable.Memory().Value() / 1024 / 1024
				}
			}

			pods, err := clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
			podRequestMap := make(map[string]map[string]int64)
			if err == nil {
				for _, p := range pods.Items {
					summary.PodsByPhase[string(p.Status.Phase)]++
					if p.Status.Phase == "Failed" {
						summary.FailedPods = append(summary.FailedPods, PodAlert{p.Name, p.Namespace})
					}
					var totalReq int64
					for _, c := range p.Spec.Containers {
						r := c.Resources.Requests.Cpu().MilliValue()
						summary.CpuRequested += r
						summary.MemRequested += c.Resources.Requests.Memory().Value() / 1024 / 1024
						totalReq += r
					}
					if podRequestMap[p.Namespace] == nil {
						podRequestMap[p.Namespace] = make(map[string]int64)
					}
					podRequestMap[p.Namespace][p.Name] = totalReq
				}
			}

			var newStats []PodStats
			mList, err := metricsClient.MetricsV1beta1().PodMetricses("").List(ctx, metav1.ListOptions{})
			if err == nil {
				func() {
					dbCtx, dbCancel := withDBTimeout(appCtx)
					defer dbCancel()
					tx, err := db.BeginTx(dbCtx, nil)
					if err != nil {
						logSQLError("begin_tx_metrics_insert", err)
						return
					}
					defer tx.Rollback()

					for _, m := range mList.Items {
						var podCPU, podMem int64
						for _, c := range m.Containers {
							podCPU += c.Usage.Cpu().MilliValue()
							podMem += c.Usage.Memory().Value() / 1024 / 1024
						}
						req, reqFound := getPodRequest(podRequestMap, m.Namespace, m.Name)
						pStat := PodStats{
							Name:              m.Name,
							Namespace:         m.Namespace,
							CPUUsage:          podCPU,
							CPURequest:        req,
							CPURequestPresent: reqFound,
							MemUsage:          podMem,
						}
						if reqFound && req > MinWasteCPURequestMCpu && podCPU < req/2 {
							saving := req - podCPU
							pStat.PotentialSavingMCpu = &saving
							pStat.Opportunity = fmt.Sprintf("-%dm", saving)
						}

						if _, err := tx.ExecContext(dbCtx, `INSERT INTO metrics (pod_name, namespace, container_name, cpu_usage, cpu_request, mem_usage, mem_request, opportunity) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
							m.Name, m.Namespace, "all", podCPU, req, podMem, 0, pStat.Opportunity); err != nil {
							logSQLError("insert_metric", err)
							slog.Warn("insert metric failed", "pod", m.Name, "namespace", m.Namespace, "err", err)
							continue
						}
						newStats = append(newStats, pStat)
					}

					if err := tx.Commit(); err != nil {
						logSQLError("commit_metrics_insert", err)
						return
					}
				}()
			}
			sort.Slice(newStats, func(i, j int) bool { return newStats[i].CPUUsage > newStats[j].CPUUsage })

			if summary.CpuAllocatable > 0 {
				summary.Efficiency = (float64(summary.CpuRequested) / float64(summary.CpuAllocatable)) * 100
			}

			statsMutex.Lock()
			latestStats = newStats
			latestSummary = summary
			statsMutex.Unlock()
			cancel()
			select {
			case <-appCtx.Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
	}()

	setSecureHeaders := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
	}
	writeJSONError := func(w http.ResponseWriter, status int, msg string) {
		setSecureHeaders(w)
		w.WriteHeader(status)
		if err := json.NewEncoder(w).Encode(map[string]string{"error": msg}); err != nil {
			slog.Error("failed to encode error response", "status", status, "err", err)
		}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/static/icon.png", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("If-None-Match") == iconETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		w.Header().Set("ETag", iconETag)
		w.Write(iconPNG)
	})

	mux.HandleFunc("/api/summary", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		setSecureHeaders(w)
		statsMutex.Lock()
		defer statsMutex.Unlock()
		if err := json.NewEncoder(w).Encode(latestSummary); err != nil {
			slog.Error("failed to encode summary response", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	})

	mux.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		setSecureHeaders(w)
		statsMutex.Lock()
		defer statsMutex.Unlock()
		if err := json.NewEncoder(w).Encode(latestStats); err != nil {
			slog.Error("failed to encode metrics response", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	})

	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		setSecureHeaders(w)

		// Parse range parameter: 30m (default), 1h, 6h, 24h, 7d, 30d, 90d, 365d
		rangeParam := r.URL.Query().Get("range")
		if rangeParam == "" {
			rangeParam = "30m"
		}

		var query string
		var timeFormat string
		var timeout time.Duration

		switch rangeParam {
		case "30m":
			// Raw metrics, minute buckets (last 30 minutes)
			query = `
				SELECT date_trunc('minute', recorded_at) AS bucket,
					SUM((CAST(cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics
				WHERE recorded_at > NOW() - INTERVAL '30 minutes'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "15:04"
			timeout = dbTimeout
		case "1h":
			// Raw metrics, minute buckets (last 1 hour)
			query = `
				SELECT date_trunc('minute', recorded_at) AS bucket,
					SUM((CAST(cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics
				WHERE recorded_at > NOW() - INTERVAL '1 hour'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "15:04"
			timeout = dbTimeout
		case "6h":
			// Raw metrics, 5-minute buckets (last 6 hours)
			query = `
				SELECT date_trunc('hour', recorded_at) + 
					INTERVAL '5 min' * (EXTRACT(minute FROM recorded_at)::INT / 5) AS bucket,
					SUM((CAST(cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics
				WHERE recorded_at > NOW() - INTERVAL '6 hours'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "15:04"
			timeout = dbTimeout * 2
		case "24h":
			// Mix: raw for recent + hourly aggregates, 15-minute buckets
			query = `
				WITH combined AS (
					SELECT date_trunc('hour', recorded_at) + 
						INTERVAL '15 min' * (EXTRACT(minute FROM recorded_at)::INT / 15) AS bucket,
						cpu_request, cpu_usage
					FROM metrics
					WHERE recorded_at > NOW() - INTERVAL '24 hours'
					UNION ALL
					SELECT hour_bucket AS bucket, avg_cpu_request AS cpu_request, avg_cpu_usage AS cpu_usage
					FROM metrics_hourly
					WHERE hour_bucket > NOW() - INTERVAL '24 hours' 
						AND hour_bucket < (SELECT MIN(recorded_at) FROM metrics)
				)
				SELECT bucket,
					SUM((CAST(cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM combined
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "15:04"
			timeout = dbTimeout * 3
		case "7d":
			// Hourly aggregates, hour buckets
			query = `
				SELECT hour_bucket AS bucket,
					SUM((CAST(avg_cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(avg_cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics_hourly
				WHERE hour_bucket > NOW() - INTERVAL '7 days'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "01/02 15:04"
			timeout = dbTimeout * 3
		case "30d":
			// Daily aggregates
			query = `
				SELECT day_bucket AS bucket,
					SUM((CAST(avg_cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(avg_cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics_daily
				WHERE day_bucket > NOW() - INTERVAL '30 days'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "01/02"
			timeout = dbTimeout * 2
		case "90d":
			// Daily aggregates
			query = `
				SELECT day_bucket AS bucket,
					SUM((CAST(avg_cpu_request AS FLOAT) * $1) / 360.0) AS req,
					SUM((CAST(avg_cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics_daily
				WHERE day_bucket > NOW() - INTERVAL '90 days'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "01/02"
			timeout = dbTimeout * 2
		case "365d":
			// Daily aggregates, weekly buckets
			query = `
				SELECT date_trunc('week', day_bucket) AS bucket,
					AVG((CAST(avg_cpu_request AS FLOAT) * $1) / 360.0) AS req,
					AVG((CAST(avg_cpu_usage AS FLOAT) * $1) / 360.0) AS use
				FROM metrics_daily
				WHERE day_bucket > NOW() - INTERVAL '365 days'
				GROUP BY bucket ORDER BY bucket ASC`
			timeFormat = "2006-01-02"
			timeout = dbTimeout * 3
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid range; valid values: 30m, 1h, 6h, 24h, 7d, 30d, 90d, 365d")
			return
		}

		queryCtx, queryCancel := context.WithTimeout(r.Context(), timeout)
		defer queryCancel()

		rows, err := db.QueryContext(queryCtx, query, USD_PER_VCPU_HOUR/1000.0)
		if err != nil {
			logSQLError("query_history", err)
			slog.Error("sql query error", "range", rangeParam, "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		defer rows.Close()

		var points []HistoryPoint
		for rows.Next() {
			var (
				bucket  time.Time
				reqCost float64
				useCost float64
			)
			if err := rows.Scan(&bucket, &reqCost, &useCost); err != nil {
				slog.Error("failed to scan history row", "err", err)
				writeJSONError(w, http.StatusInternalServerError, "internal server error")
				return
			}
			points = append(points, HistoryPoint{
				Time:    bucket.Format(timeFormat),
				ReqCost: reqCost,
				UseCost: useCost,
			})
		}
		if err := rows.Err(); err != nil {
			logSQLError("history_rows_iteration", err)
			slog.Error("history row iteration failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
		if err := json.NewEncoder(w).Encode(points); err != nil {
			slog.Error("failed to encode history response", "err", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
			return
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data:")
		_, _ = w.Write(dashboardHTML)
	})

	mux.HandleFunc("/static/dashboard.css", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(dashboardCSS)
	})

	mux.HandleFunc("/static/dashboard.js", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(dashboardJS)
	})

	listenAddr := getEnv("LISTEN_ADDR", "127.0.0.1:8080")
	rateLimit := getEnvInt("RATE_LIMIT_RPS", 100)

	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      withMiddleware(mux, recoverMiddleware, requestLoggerMiddleware, rateLimitMiddleware(rateLimit)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("Sentinel Cluster Overview", "url", fmt.Sprintf("http://%s", listenAddr))

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-sigChan
	slog.Info("shutting down gracefully...")
	appCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown error", "err", err)
	}
	if err := db.Close(); err != nil {
		slog.Warn("database close failed", "err", err)
	}
}
