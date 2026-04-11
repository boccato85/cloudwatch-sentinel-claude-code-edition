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
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	_ "github.com/lib/pq"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
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
				slog.Error("panic recovered", "request_id", r.Header.Get("X-Request-ID"), "method", r.Method, "path", r.URL.Path, "panic", rec)
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
	sslMode := getEnv("DB_SSLMODE", "disable")
	dbTimeout = time.Duration(getEnvInt("DB_TIMEOUT_SEC", 5)) * time.Second
	if sslMode == "disable" {
		slog.Warn("PostgreSQL SSL is disabled — set DB_SSLMODE=require for production")
	}

	connStr := fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=%s",
		dbHost, dbUser, dbPass, dbName, sslMode)

	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	pingCtx, pingCancel := withDBTimeout(context.Background())
	defer pingCancel()
	if err = db.PingContext(pingCtx); err != nil {
		slog.Error("database ping failed", "err", err)
		os.Exit(1)
	}

	slog.Info("Sentinel Intelligence Engine: Active")

	home := homedir.HomeDir()
	k8sCfg, err := clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
	if err != nil {
		slog.Error("failed to load kubeconfig", "err", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(k8sCfg)
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
		queryCtx, queryCancel := withDBTimeout(r.Context())
		defer queryCancel()
		rows, err := db.QueryContext(queryCtx, `
			SELECT
				date_trunc('minute', timestamp) AS bucket,
				SUM((CAST(cpu_request AS FLOAT) * $1) / 360.0) AS req,
				SUM((CAST(cpu_usage AS FLOAT) * $1) / 360.0) AS use
			FROM metrics
			WHERE timestamp > NOW() - INTERVAL '30 minutes'
			GROUP BY bucket
			ORDER BY bucket ASC
			LIMIT 100`, USD_PER_VCPU_HOUR/1000.0)
		if err != nil {
			logSQLError("query_history", err)
			slog.Error("sql query error", "err", err)
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
				Time:    bucket.Format("15:04:05"),
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

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      withMiddleware(mux, recoverMiddleware, requestLoggerMiddleware),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	slog.Info("Sentinel Cluster Overview", "url", "http://localhost:8080")

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
