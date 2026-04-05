package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	_ "github.com/lib/pq"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

const (
	USD_PER_VCPU_HOUR = 0.04
	USD_PER_GB_HOUR   = 0.005
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
	Nodes           []NodeInfo     `json:"nodes"`
	PodsByPhase     map[string]int `json:"podsByPhase"`
	FailedPods      []PodAlert     `json:"failedPods"`
	PendingPods     []PodAlert     `json:"pendingPods"`
	CpuAllocatable  int64          `json:"cpuAllocatable"`
	CpuRequested    int64          `json:"cpuRequested"`
	MemAllocatable  int64          `json:"memAllocatable"`
	MemRequested    int64          `json:"memRequested"`
	Efficiency      float64        `json:"efficiency"`
}

type HistoryPoint struct {
	Time    string  `json:"time"`
	ReqCost float64 `json:"reqCost"`
	UseCost float64 `json:"useCost"`
}

type PodStats struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	CPUUsage    int64  `json:"cpuUsage"`
	CPURequest  int64  `json:"cpuRequest"`
	MemUsage    int64  `json:"memUsage"`
	Opportunity string `json:"opportunity"`
}

var (
	latestStats   []PodStats
	latestSummary ClusterSummary
	statsMutex    sync.Mutex
	db            *sql.DB
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	dbUser := getEnv("DB_USER", "boccatosantos")
	dbPass := getEnv("DB_PASSWORD", "sentinel")
	dbName := getEnv("DB_NAME", "sentinel_db")
	dbHost := getEnv("DB_HOST", "localhost")
	sslMode := getEnv("DB_SSLMODE", "disable")

	connStr := fmt.Sprintf("host=%s user=%s password=%s dbname=%s sslmode=%s", 
		dbHost, dbUser, dbPass, dbName, sslMode)
	
	var err error
	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatalf("❌ CRITICAL: Database connection failed: %v", err)
	}
	if err = db.Ping(); err != nil {
		log.Fatalf("❌ CRITICAL: Database ping failed: %v", err)
	}
	
	fmt.Println("✅ Sentinel Intelligence Engine: Active.")

	home := homedir.HomeDir()
	config, err := clientcmd.BuildConfigFromFlags("", filepath.Join(home, ".kube", "config"))
	if err != nil {
		log.Fatalf("❌ CRITICAL: Failed to load kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("❌ CRITICAL: Failed to create k8s client: %v", err)
	}
	metricsClient, err := metricsv.NewForConfig(config)
	if err != nil {
		log.Fatalf("❌ CRITICAL: Failed to create metrics client: %v", err)
	}

	go func() {
		for {
			summary := ClusterSummary{PodsByPhase: make(map[string]int)}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			
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
				// OPT-01: Iniciar Transação para Batch Insert
				tx, err := db.Begin()
				if err != nil {
					log.Printf("⚠️ WARN: Transaction failed: %v", err)
				} else {
					for _, m := range mList.Items {
						var podCPU, podMem int64
						for _, c := range m.Containers {
							podCPU += c.Usage.Cpu().MilliValue()
							podMem += c.Usage.Memory().Value() / 1024 / 1024
						}
						req := podRequestMap[m.Namespace][m.Name]
						pStat := PodStats{Name: m.Name, Namespace: m.Namespace, CPUUsage: podCPU, CPURequest: req, MemUsage: podMem}
						if req > 5 && podCPU < req/2 {
							pStat.Opportunity = fmt.Sprintf("-%dm", req-podCPU)
						}
						
						_, _ = tx.Exec(`INSERT INTO metrics (pod_name, namespace, container_name, cpu_usage, cpu_request, mem_usage, mem_request, opportunity) 
							VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, m.Name, m.Namespace, "all", podCPU, req, podMem, 0, pStat.Opportunity)
						newStats = append(newStats, pStat)
					}
					tx.Commit()
				}
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
			time.Sleep(10 * time.Second)
		}
	}()

	setSecureHeaders := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:8080")
	}

	http.HandleFunc("/api/summary", func(w http.ResponseWriter, r *http.Request) {
		setSecureHeaders(w)
		statsMutex.Lock()
		defer statsMutex.Unlock()
		json.NewEncoder(w).Encode(latestSummary)
	})

	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		setSecureHeaders(w)
		statsMutex.Lock()
		defer statsMutex.Unlock()
		json.NewEncoder(w).Encode(latestStats)
	})

	http.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		setSecureHeaders(w)
		rows, err := db.Query(`
			SELECT t, SUM(req) as req, SUM(use) as use FROM (
				SELECT TO_CHAR(timestamp, 'HH24:MI:SS') as t, 
				       (CAST(cpu_request AS FLOAT) * $1) / 360.0 as req,
				       (CAST(cpu_usage AS FLOAT) * $1) / 360.0 as use
				FROM metrics WHERE timestamp > NOW() - INTERVAL '30 minutes'
			) sub GROUP BY t ORDER BY t ASC LIMIT 100`, USD_PER_VCPU_HOUR/1000.0)
		if err != nil {
			log.Printf("⚠️ SQL Error: %v", err)
			return
		}
		defer rows.Close()
		var points []HistoryPoint
		for rows.Next() {
			var p HistoryPoint
			if err := rows.Scan(&p.Time, &p.ReqCost, &p.UseCost); err != nil {
				continue
			}
			points = append(points, p)
		}
		json.NewEncoder(w).Encode(points)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Sentinel | Cluster Observatory</title>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.0/dist/chart.umd.min.js"></script>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
<style>
*{box-sizing:border-box;margin:0;padding:0}
:root{
  --bg:#0f1117;--surface:#1a1e27;--surface2:#222735;--border:#2d3347;
  --cyan:#00b4ff;--green:#00cc8f;--red:#e54949;--orange:#f5a623;
  --purple:#a855f7;--yellow:#fbbf24;--pink:#ec4899;
  --text:#c8d0e0;--text-dim:#7a8499;--text-bright:#edf0f7;
}
body{font-family:'Inter',sans-serif;background:var(--bg);color:var(--text);font-size:13px;min-height:100vh}
/* HEADER */
.hdr{background:#090c12;border-bottom:1px solid var(--border);padding:0 20px;height:50px;display:flex;align-items:center;justify-content:space-between;position:sticky;top:0;z-index:100}
.logo{font-size:1em;font-weight:700;color:var(--cyan);letter-spacing:2px;text-transform:uppercase;display:flex;align-items:center;gap:8px}
.logo-tri{width:0;height:0;border-left:8px solid transparent;border-right:8px solid transparent;border-bottom:14px solid var(--cyan)}
.ctag{background:var(--surface2);border:1px solid var(--border);border-radius:4px;padding:3px 10px;font-size:0.75em;color:var(--text-dim);margin-left:14px}
.hdr-r{display:flex;align-items:center;gap:16px;color:var(--text-dim);font-size:0.78em}
.spill{display:flex;align-items:center;gap:6px;background:rgba(0,204,143,.1);border:1px solid rgba(0,204,143,.3);border-radius:20px;padding:3px 12px;color:var(--green);font-size:0.78em;font-weight:500}
.sdot{width:7px;height:7px;border-radius:50%;background:var(--green);animation:pulse 2s infinite}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.35}}
/* LAYOUT */
.main{padding:14px;display:flex;flex-direction:column;gap:10px}
/* KPI ROW */
.kpi-row{display:grid;grid-template-columns:repeat(6,1fr);gap:10px}
.kpi{background:var(--surface);border:1px solid var(--border);border-radius:6px;padding:14px 16px;position:relative;overflow:hidden}
.kpi::before{content:'';position:absolute;top:0;left:0;right:0;height:2px}
.kpi.c-cyan::before{background:var(--cyan)}
.kpi.c-green::before{background:var(--green)}
.kpi.c-red::before{background:var(--red)}
.kpi.c-orange::before{background:var(--orange)}
.kpi.c-purple::before{background:var(--purple)}
.kpi.c-yellow::before{background:var(--yellow)}
.kpi-lbl{font-size:.7em;color:var(--text-dim);text-transform:uppercase;letter-spacing:.8px;margin-bottom:6px}
.kpi-val{font-size:1.85em;font-weight:700;color:var(--text-bright);font-family:'JetBrains Mono',monospace;line-height:1}
.kpi-sub{font-size:.72em;color:var(--text-dim);margin-top:5px}
/* GRID */
.row-3{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.row-2{display:grid;grid-template-columns:1fr 1fr;gap:10px}
/* PANEL */
.panel{background:var(--surface);border:1px solid var(--border);border-radius:6px;overflow:hidden}
.ph{padding:9px 16px;border-bottom:1px solid var(--border);display:flex;align-items:center;justify-content:space-between;background:rgba(0,0,0,.18)}
.ph-title{font-size:.72em;font-weight:600;text-transform:uppercase;letter-spacing:.9px;color:var(--text-dim)}
.ph-meta{font-size:.7em;color:var(--text-dim)}
.pb{padding:14px 16px}
.badge{font-size:.7em;padding:2px 9px;border-radius:20px;font-weight:500}
.b-ok{background:rgba(0,204,143,.14);color:var(--green);border:1px solid rgba(0,204,143,.28)}
.b-warn{background:rgba(245,166,35,.14);color:var(--orange);border:1px solid rgba(245,166,35,.28)}
.b-crit{background:rgba(229,73,73,.14);color:var(--red);border:1px solid rgba(229,73,73,.28)}
/* HONEYCOMB */
.hcomb{display:flex;gap:5px;flex-wrap:wrap}
.hex{width:28px;height:28px;clip-path:polygon(25% 0%,75% 0%,100% 50%,75% 100%,25% 100%,0% 50%);display:flex;align-items:center;justify-content:center;font-size:7px;font-weight:700;color:#000;cursor:default;transition:transform .15s}
.hex:hover{transform:scale(1.2)}
.hex.ok{background:var(--green)}
.hex.issue{background:var(--red)}
.hleg{margin-top:10px;display:flex;gap:14px;font-size:.75em;color:var(--text-dim)}
.hleg span{display:flex;align-items:center;gap:5px}
.hleg b{width:8px;height:8px;border-radius:1px;display:inline-block}
/* DONUT LAYOUT */
.dnut-wrap{display:flex;align-items:center;gap:16px}
.dnut-canvas{position:relative;width:120px;height:120px;flex-shrink:0}
.legend{font-size:.78em}
.li{display:flex;align-items:center;gap:6px;margin-bottom:5px}
.li-dot{width:8px;height:8px;border-radius:50%;flex-shrink:0}
.li-lbl{color:var(--text-dim)}
.li-val{font-family:'JetBrains Mono',monospace;color:var(--text-bright);margin-left:auto;padding-left:12px}
/* ALERTS */
.alert{display:flex;align-items:flex-start;gap:8px;padding:8px 10px;border-radius:4px;margin-bottom:6px;font-size:.82em}
.alert.failed{background:rgba(229,73,73,.09);border-left:3px solid var(--red)}
.alert.pending{background:rgba(245,166,35,.09);border-left:3px solid var(--orange)}
.alert.ok{background:rgba(0,204,143,.07);border-left:3px solid var(--green)}
.alert-ico{margin-top:1px;flex-shrink:0}
.alert-ns{font-size:.76em;color:var(--text-dim);margin-top:2px}
/* CPU SECTION */
.cpu-side{flex:1;display:flex;flex-direction:column;gap:10px}
.bar-row{display:flex;flex-direction:column;gap:4px}
.bar-head{display:flex;justify-content:space-between;font-size:.76em}
.bar-head span{color:var(--text-dim)}
.bar-head em{font-style:normal;font-family:'JetBrains Mono',monospace;color:var(--text-bright)}
.bar-bg{height:4px;background:var(--border);border-radius:2px;overflow:hidden}
.bar-fill{height:100%;border-radius:2px;transition:width .5s}
.eff-big{font-size:2em;font-weight:700;color:var(--cyan);font-family:'JetBrains Mono',monospace;margin-top:6px}
.eff-lbl{font-size:.73em;color:var(--text-dim)}
/* WASTE */
.waste-item{padding:10px 12px;border:1px solid var(--border);border-radius:4px;margin-bottom:8px;background:rgba(245,166,35,.04)}
.waste-name{font-weight:600;font-size:.84em;color:var(--text-bright);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.waste-row{display:flex;justify-content:space-between;margin-top:4px;font-size:.76em}
.waste-save{color:var(--orange);font-family:'JetBrains Mono',monospace}
.waste-bar{margin-top:6px;height:3px;background:var(--border);border-radius:2px;overflow:hidden}
.waste-fill{height:100%;background:linear-gradient(90deg,var(--orange),var(--yellow));border-radius:2px}
/* TABLE */
.wtable{width:100%;border-collapse:collapse}
.wtable th{font-size:.7em;color:var(--text-dim);text-transform:uppercase;letter-spacing:.8px;text-align:left;padding:7px 10px;border-bottom:1px solid var(--border);white-space:nowrap}
.wtable td{padding:7px 10px;border-bottom:1px solid rgba(45,51,71,.45);font-size:.84em}
.wtable tr:last-child td{border-bottom:none}
.wtable tbody tr:hover td{background:rgba(255,255,255,.018)}
.ns-tag{background:var(--surface2);padding:2px 7px;border-radius:3px;font-size:.76em}
.util-wrap{display:flex;align-items:center;gap:6px;min-width:100px}
.util-bg{flex:1;height:4px;background:var(--border);border-radius:2px;overflow:hidden}
.util-fill{height:100%;border-radius:2px;transition:width .5s}
.util-pct{font-size:.74em;color:var(--text-dim);width:34px;text-align:right}
.mono{font-family:'JetBrains Mono',monospace}
/* LINE CHART */
.line-area{height:190px;width:100%}
.line-legend{display:flex;gap:16px;font-size:.74em}
.line-legend span{display:flex;align-items:center;gap:5px}
.line-legend i{width:14px;height:2px;border-radius:2px;display:inline-block}
</style>
</head>
<body>
<!-- HEADER -->
<div class="hdr">
  <div style="display:flex;align-items:center">
    <div class="logo"><div class="logo-tri"></div>SENTINEL</div>
    <div class="ctag">minikube / local</div>
  </div>
  <div class="hdr-r">
    <div class="spill"><div class="sdot"></div>Connected</div>
    <span id="lastUp">Updating...</span>
  </div>
</div>

<div class="main">

  <!-- KPI STRIP -->
  <div class="kpi-row">
    <div class="kpi c-cyan">
      <div class="kpi-lbl">Total Nodes</div>
      <div class="kpi-val" id="kN">--</div>
      <div class="kpi-sub" id="kNs">Checking...</div>
    </div>
    <div class="kpi c-green">
      <div class="kpi-lbl">Running Pods</div>
      <div class="kpi-val" id="kR">--</div>
      <div class="kpi-sub" id="kRs">of -- total</div>
    </div>
    <div class="kpi c-red">
      <div class="kpi-lbl">Failed / Pending</div>
      <div class="kpi-val" id="kF">--</div>
      <div class="kpi-sub" id="kFs">-- pending</div>
    </div>
    <div class="kpi c-orange">
      <div class="kpi-lbl">CPU Efficiency</div>
      <div class="kpi-val" id="kE">--%</div>
      <div class="kpi-sub" id="kEs">req / alloc</div>
    </div>
    <div class="kpi c-purple">
      <div class="kpi-lbl">Top CPU Consumer</div>
      <div class="kpi-val mono" id="kT">--m</div>
      <div class="kpi-sub" id="kTs" style="overflow:hidden;text-overflow:ellipsis;white-space:nowrap">--</div>
    </div>
    <div class="kpi c-yellow">
      <div class="kpi-lbl">Waste Opportunities</div>
      <div class="kpi-val" id="kW">--</div>
      <div class="kpi-sub">pods oversized</div>
    </div>
  </div>

  <!-- ROW: NODE MAP | POD PHASES | ALERTS -->
  <div class="row-3">

    <div class="panel">
      <div class="ph"><span class="ph-title">Node Health Map</span><span class="badge b-ok" id="nbadge">OK</span></div>
      <div class="pb">
        <div class="hcomb" id="honeycomb"></div>
        <div class="hleg">
          <span><b style="background:var(--green)"></b>Healthy</span>
          <span><b style="background:var(--red)"></b>Issue</span>
        </div>
      </div>
    </div>

    <div class="panel">
      <div class="ph"><span class="ph-title">Pod Distribution</span></div>
      <div class="pb">
        <div class="dnut-wrap">
          <div class="dnut-canvas"><canvas id="phaseDonut"></canvas></div>
          <div class="legend" id="phaseLegend"></div>
        </div>
      </div>
    </div>

    <div class="panel">
      <div class="ph"><span class="ph-title">Active Alerts</span><span class="badge b-ok" id="abadge">0 Issues</span></div>
      <div class="pb" id="alertsBox">
        <div class="alert ok"><span class="alert-ico" style="color:var(--green)">&#10003;</span><div>Cluster Healthy</div></div>
      </div>
    </div>

  </div>

  <!-- ROW: CPU ALLOCATION | FINOPS -->
  <div class="row-2">

    <div class="panel">
      <div class="ph"><span class="ph-title">CPU Resource Allocation</span><span class="badge b-ok" id="cpubadge">Optimal</span></div>
      <div class="pb">
        <div class="dnut-wrap">
          <div class="dnut-canvas"><canvas id="cpuDonut"></canvas></div>
          <div class="cpu-side">
            <div class="bar-row">
              <div class="bar-head"><span>Requested</span><em id="cpuReqV">--m</em></div>
              <div class="bar-bg"><div class="bar-fill" id="cpuReqB" style="width:0%;background:var(--cyan)"></div></div>
            </div>
            <div class="bar-row">
              <div class="bar-head"><span>Allocatable</span><em id="cpuAlcV">--m</em></div>
              <div class="bar-bg"><div class="bar-fill" style="width:100%;background:rgba(45,51,71,.8)"></div></div>
            </div>
            <div class="eff-big" id="effBig">--%</div>
            <div class="eff-lbl">efficiency ratio</div>
          </div>
        </div>
      </div>
    </div>

    <div class="panel">
      <div class="ph"><span class="ph-title">Waste Intelligence (FinOps)</span><span class="badge b-warn" id="wcnt">Scanning...</span></div>
      <div class="pb" id="wasteList">
        <div style="color:var(--text-dim);font-size:.84em">Collecting metrics...</div>
      </div>
    </div>

  </div>

  <!-- TOP WORKLOADS TABLE -->
  <div class="panel">
    <div class="ph">
      <span class="ph-title">Top Workloads by CPU Consumption</span>
      <span class="ph-meta">Live &#183; 5s refresh</span>
    </div>
    <div class="pb" style="padding:0 16px 10px">
      <table class="wtable">
        <thead><tr>
          <th style="width:30px">#</th>
          <th>Pod Name</th>
          <th>Namespace</th>
          <th>CPU Usage</th>
          <th>CPU Request</th>
          <th>Utilization</th>
          <th>Waste</th>
        </tr></thead>
        <tbody id="wbody"><tr><td colspan="7" style="text-align:center;color:var(--text-dim);padding:20px">Collecting data...</td></tr></tbody>
      </table>
    </div>
  </div>

  <!-- FINANCIAL TIMELINE -->
  <div class="panel">
    <div class="ph">
      <span class="ph-title">Financial Correlation &#8212; ROI Timeline (last 30 min)</span>
      <div class="line-legend">
        <span><i style="background:var(--red)"></i>Budget (Requested)</span>
        <span><i style="background:var(--green)"></i>Actual (Usage)</span>
      </div>
    </div>
    <div class="pb"><div class="line-area"><canvas id="mainLineChart"></canvas></div></div>
  </div>

</div><!-- /main -->

<script>
var charts = {};
var PCOLS = ['#00cc8f','#00b4ff','#e54949','#fbbf24','#a855f7','#f5a623','#ec4899'];

function esc(s) {
  var d = document.createElement('div');
  d.appendChild(document.createTextNode(String(s)));
  return d.innerHTML;
}

function uDonut(id, labels, data, colors) {
  var el = document.getElementById(id);
  if (!el) return;
  if (charts[id]) {
    charts[id].data.labels = labels;
    charts[id].data.datasets[0].data = data;
    charts[id].data.datasets[0].backgroundColor = colors;
    charts[id].update('none');
  } else {
    charts[id] = new Chart(el, {
      type: 'doughnut',
      data: { labels: labels, datasets: [{ data: data, backgroundColor: colors, borderWidth: 0, hoverOffset: 4 }] },
      options: {
        cutout: '76%',
        plugins: { legend: { display: false } },
        maintainAspectRatio: false
      }
    });
  }
}

function uLine(id, hData) {
  var el = document.getElementById(id);
  if (!el || !hData || hData.length === 0) return;
  if (charts[id]) {
    charts[id].data.labels = hData.map(function(p){ return p.time; });
    charts[id].data.datasets[0].data = hData.map(function(p){ return p.reqCost; });
    charts[id].data.datasets[1].data = hData.map(function(p){ return p.useCost; });
    charts[id].update('none');
  } else {
    charts[id] = new Chart(el, {
      type: 'line',
      data: {
        labels: hData.map(function(p){ return p.time; }),
        datasets: [
          { label: 'Budget ($)', borderColor: '#e54949', borderWidth: 1.5,
            data: hData.map(function(p){ return p.reqCost; }),
            pointRadius: 0, tension: 0.3, fill: false },
          { label: 'Actual ($)', borderColor: '#00cc8f', borderWidth: 1.5,
            data: hData.map(function(p){ return p.useCost; }),
            fill: true, backgroundColor: 'rgba(0,204,143,.06)', pointRadius: 0, tension: 0.3 }
        ]
      },
      options: {
        responsive: true, maintainAspectRatio: false,
        interaction: { mode: 'index', intersect: false },
        plugins: {
          legend: { display: false },
          tooltip: { backgroundColor: '#1a1e27', borderColor: '#2d3347', borderWidth: 1,
                     titleColor: '#c8d0e0', bodyColor: '#7a8499' }
        },
        scales: {
          y: { grid: { color: 'rgba(45,51,71,.55)' },
               ticks: { 
                 color: '#7a8499', 
                 font: { family: 'JetBrains Mono', size: 10 },
                 callback: function(value) {
                   return '$' + value.toFixed(6);
                 }
               } 
          },
          x: { grid: { display: false },
               ticks: { color: '#7a8499', maxTicksLimit: 8, font: { size: 10 } } }
        }
      }
    });
  }
}

async function update() {
  try {
    var s = await (await fetch('/api/summary')).json();
    var nodes    = s.nodes || [];
    var byPhase  = s.podsByPhase || {};
    var failed   = s.failedPods || [];
    var pending  = s.pendingPods || [];
    var eff      = s.efficiency || 0;
    var running  = byPhase['Running'] || 0;
    var total    = Object.values(byPhase).reduce(function(a,b){ return a+b; }, 0);
    var issues   = nodes.filter(function(n){ return n.status !== 'Running'; }).length;

    document.getElementById('kN').textContent   = nodes.length;
    document.getElementById('kNs').textContent  = issues > 0 ? issues + ' with issues' : 'All healthy';
    document.getElementById('kR').textContent   = running;
    document.getElementById('kRs').textContent  = 'of ' + total + ' total';
    document.getElementById('kF').textContent   = failed.length;
    document.getElementById('kFs').textContent  = pending.length + ' pending';
    document.getElementById('kE').textContent   = eff.toFixed(1) + '%';
    document.getElementById('kEs').textContent  = s.cpuRequested + 'm / ' + s.cpuAllocatable + 'm';
    document.getElementById('effBig').textContent = eff.toFixed(1) + '%';
    document.getElementById('cpuReqV').textContent = s.cpuRequested + 'm';
    document.getElementById('cpuAlcV').textContent = s.cpuAllocatable + 'm';

    var reqPct = s.cpuAllocatable > 0 ? (s.cpuRequested / s.cpuAllocatable * 100) : 0;
    var rb = document.getElementById('cpuReqB');
    rb.style.width = Math.min(reqPct, 100) + '%';
    rb.style.background = reqPct > 85 ? 'var(--red)' : reqPct > 70 ? 'var(--orange)' : 'var(--cyan)';

    var cpuBadge = document.getElementById('cpubadge');
    cpuBadge.textContent = eff > 85 ? 'Critical' : eff > 70 ? 'High Load' : 'Optimal';
    cpuBadge.className = 'badge ' + (eff > 85 ? 'b-crit' : eff > 70 ? 'b-warn' : 'b-ok');

    uDonut('cpuDonut',
      ['Requested','Free'],
      [s.cpuRequested, Math.max(0, s.cpuAllocatable - s.cpuRequested)],
      ['#00b4ff','#2d3347']
    );

    var hc = document.getElementById('honeycomb');
    hc.innerHTML = '';
    nodes.forEach(function(n) {
      var d = document.createElement('div');
      d.className = 'hex ' + (n.status === 'Running' ? 'ok' : 'issue');
      d.title = n.name;
      d.textContent = 'N';
      hc.appendChild(d);
    });
    var nb = document.getElementById('nbadge');
    nb.textContent = issues > 0 ? issues + ' Issues' : 'All OK';
    nb.className = 'badge ' + (issues > 0 ? 'b-crit' : 'b-ok');

    var phases = Object.keys(byPhase);
    var phaseVals = Object.values(byPhase);
    uDonut('phaseDonut', phases, phaseVals, PCOLS.slice(0, phases.length));
    var leg = '';
    phases.forEach(function(ph, i) {
      leg += '<div class="li"><div class="li-dot" style="background:' + PCOLS[i] + '"></div>' +
             '<span class="li-lbl">' + esc(ph) + '</span>' +
             '<span class="li-val">' + phaseVals[i] + '</span></div>';
    });
    document.getElementById('phaseLegend').innerHTML = leg;

    var ahtml = '';
    failed.forEach(function(p) {
      ahtml += '<div class="alert failed"><span class="alert-ico" style="color:var(--red)">&#9888;</span>' +
               '<div><b>' + esc(p.name) + '</b><div class="alert-ns">' + esc(p.namespace) + ' &bull; FAILED</div></div></div>';
    });
    pending.forEach(function(p) {
      ahtml += '<div class="alert pending"><span class="alert-ico" style="color:var(--orange)">&#9203;</span>' +
               '<div><b>' + esc(p.name) + '</b><div class="alert-ns">' + esc(p.namespace) + ' &bull; PENDING</div></div></div>';
    });
    document.getElementById('alertsBox').innerHTML = ahtml ||
      '<div class="alert ok"><span style="color:var(--green)">&#10003;</span>&nbsp; No active alerts &mdash; cluster healthy</div>';
    var totalA = failed.length + pending.length;
    var ab = document.getElementById('abadge');
    ab.textContent = totalA > 0 ? totalA + ' Issues' : '0 Issues';
    ab.className = 'badge ' + (failed.length > 0 ? 'b-crit' : totalA > 0 ? 'b-warn' : 'b-ok');

    var m = await (await fetch('/api/metrics')).json();
    m = m || [];
    if (m.length > 0) {
      document.getElementById('kT').textContent  = m[0].cpuUsage + 'm';
      document.getElementById('kTs').textContent = m[0].name || '--';
    }
    var maxCpu = m.length > 0 ? m[0].cpuUsage : 1;
    var rows = '';
    m.slice(0, 10).forEach(function(p, i) {
      var pct = maxCpu > 0 ? (p.cpuUsage / maxCpu * 100) : 0;
      var fc = pct > 80 ? 'var(--red)' : pct > 55 ? 'var(--orange)' : 'var(--cyan)';
      var opp = p.opportunity
        ? '<span style="color:var(--orange);font-family:monospace">' + esc(p.opportunity) + ' CPU</span>'
        : '<span style="color:var(--green)">&#10003;</span>';
      rows += '<tr>' +
        '<td style="color:var(--text-dim)">' + (i+1) + '</td>' +
        '<td class="mono" style="max-width:180px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">' + esc(p.name||'--') + '</td>' +
        '<td><span class="ns-tag">' + esc(p.namespace||'--') + '</span></td>' +
        '<td class="mono" style="color:var(--cyan)">' + p.cpuUsage + 'm</td>' +
        '<td class="mono" style="color:var(--text-dim)">' + p.cpuRequest + 'm</td>' +
        '<td><div class="util-wrap"><div class="util-bg"><div class="util-fill" style="width:' + pct.toFixed(0) + '%;background:' + fc + '"></div></div>' +
            '<span class="util-pct">' + pct.toFixed(0) + '%</span></div></td>' +
        '<td>' + opp + '</td>' +
        '</tr>';
    });
    document.getElementById('wbody').innerHTML = rows ||
      '<tr><td colspan="7" style="text-align:center;color:var(--text-dim);padding:16px">No workload data</td></tr>';

    var waste = m.filter(function(p){ return p.opportunity; });
    document.getElementById('kW').textContent = waste.length;
    var wc = document.getElementById('wcnt');
    wc.textContent = waste.length + ' item' + (waste.length !== 1 ? 's' : '');
    wc.className = 'badge ' + (waste.length > 0 ? 'b-warn' : 'b-ok');
    if (waste.length > 0) {
      document.getElementById('wasteList').innerHTML = waste.slice(0, 5).map(function(p) {
        return '<div class="waste-item"><div class="waste-name">' + esc(p.name) + '</div>' +
               '<div class="waste-row"><span style="color:var(--text-dim)">Savings opportunity</span>' +
               '<span class="waste-save">' + esc(p.opportunity) + ' CPU</span></div>' +
               '<div class="waste-bar"><div class="waste-fill" style="width:65%"></div></div></div>';
      }).join('');
    } else {
      document.getElementById('wasteList').innerHTML =
        '<div class="alert ok"><span style="color:var(--green)">&#10003;</span>&nbsp; All workloads rightsized</div>';
    }

    var h = await (await fetch('/api/history')).json();
    uLine('mainLineChart', h);

    document.getElementById('lastUp').textContent = 'Updated: ' + new Date().toLocaleTimeString();
  } catch(e) { console.error('Sentinel update error:', e); }
}
setInterval(update, 5000);
update();
</script>
</body>
</html>`)
	})

	srv := &http.Server{
		Addr:         ":8080",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	fmt.Println("🌐 Sentinel Cluster Overview: http://localhost:8080")
	log.Fatal(srv.ListenAndServe())
}
