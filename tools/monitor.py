#!/usr/bin/env python3
"""
monitor.py — Coleta de métricas via Sentinel Go Agent
sentinel

Coleta dados do Go agent standalone:
  - /api/summary — estado geral do cluster (nodes, pods, CPU)
  - /api/metrics — métricas detalhadas por pod

Saída: JSON em stdout para consumo pelo benchmark.

Uso:
    python3 tools/monitor.py
"""

import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests
import yaml

SENTINEL_URL = os.environ.get("SENTINEL_URL", "http://localhost:8080")
THRESHOLDS_FILE = os.path.join(os.path.dirname(__file__), "..", "config", "thresholds.yaml")


def load_thresholds() -> dict:
    with open(THRESHOLDS_FILE, encoding="utf-8") as f:
        return yaml.safe_load(f)


def get_sentinel_summary() -> dict:
    """Fetch cluster summary from Go agent."""
    try:
        resp = requests.get(f"{SENTINEL_URL}/api/summary", timeout=10)
        resp.raise_for_status()
        return {"data": resp.json(), "error": None}
    except requests.RequestException as exc:
        return {"data": None, "error": str(exc)}


def get_sentinel_metrics() -> dict:
    """Fetch per-pod metrics from Go agent."""
    try:
        resp = requests.get(f"{SENTINEL_URL}/api/metrics", timeout=10)
        resp.raise_for_status()
        return {"data": resp.json(), "error": None}
    except requests.RequestException as exc:
        return {"data": None, "error": str(exc)}


def classify_metric(value, warning_threshold, critical_threshold) -> str:
    if value is None:
        return "UNKNOWN"
    if value > critical_threshold:
        return "CRITICAL"
    if value > warning_threshold:
        return "WARNING"
    return "OK"


def extract_metrics_from_summary(summary: dict, thresholds: dict) -> dict:
    """Extract and classify resource metrics from summary."""
    metrics = {}
    
    # CPU usage from summary
    cpu_val = summary.get("total_cpu_usage_millicores")
    cpu_req = summary.get("total_cpu_request_millicores")
    if cpu_val is not None and cpu_req is not None and cpu_req > 0:
        cpu_pct = round((cpu_val / cpu_req) * 100, 2)
    else:
        cpu_pct = None
    
    th_cpu = thresholds.get("cpu", {})
    metrics["cpu_usage_percent"] = {
        "value": cpu_pct,
        "error": None if cpu_pct is not None else "No CPU data",
        "status": classify_metric(cpu_pct, th_cpu.get("warning", 70), th_cpu.get("critical", 85)),
    }
    
    # Memory usage from summary
    mem_val = summary.get("total_memory_usage_bytes")
    mem_req = summary.get("total_memory_request_bytes")
    if mem_val is not None and mem_req is not None and mem_req > 0:
        mem_pct = round((mem_val / mem_req) * 100, 2)
    else:
        mem_pct = None
    
    th_mem = thresholds.get("memory", {})
    metrics["memory_usage_percent"] = {
        "value": mem_pct,
        "error": None if mem_pct is not None else "No memory data",
        "status": classify_metric(mem_pct, th_mem.get("warning", 75), th_mem.get("critical", 90)),
    }
    
    # Disk is not tracked by Sentinel agent (node-level metric)
    # Mark as N/A
    metrics["disk_usage_percent"] = {
        "value": None,
        "error": "Disk metrics not collected by Sentinel agent",
        "status": "UNKNOWN",
    }
    
    return metrics


def extract_pods_from_summary(summary: dict) -> list:
    """Extract pod status from summary."""
    pods = []
    pod_count = summary.get("pod_count", 0)
    running_pods = summary.get("running_pods", 0)
    pending_pods = summary.get("pending_pods", 0)
    failed_pods = summary.get("failed_pods", 0)
    
    # Summary-level info (detailed pod list comes from /api/metrics)
    pods.append({
        "summary": True,
        "total": pod_count,
        "running": running_pods,
        "pending": pending_pods,
        "failed": failed_pods,
    })
    
    return pods


def extract_detailed_pods(metrics_data: list) -> list:
    """Extract detailed pod info from /api/metrics response."""
    pods = []
    for m in metrics_data or []:
        pods.append({
            "namespace": m.get("namespace", "unknown"),
            "name": m.get("pod", "unknown"),
            "phase": "Running" if m.get("cpu_usage_millicores") else "Unknown",
            "cpu_millicores": m.get("cpu_usage_millicores"),
            "memory_bytes": m.get("memory_usage_bytes"),
            "restarts": 0,  # Not tracked by metrics endpoint
            "waiting_reason": None,
        })
    return pods


def main():
    thresholds = load_thresholds()

    with ThreadPoolExecutor(max_workers=2) as pool:
        future_summary = pool.submit(get_sentinel_summary)
        future_metrics = pool.submit(get_sentinel_metrics)

    summary_result = future_summary.result()
    metrics_result = future_metrics.result()
    
    # Handle errors
    if summary_result["error"]:
        print(json.dumps({
            "error": f"Failed to connect to Sentinel agent: {summary_result['error']}",
            "hint": "Is the Go agent running? Start with: cd agent && make start",
        }, indent=2))
        sys.exit(1)
    
    summary = summary_result["data"]
    metrics_data = metrics_result["data"] if not metrics_result["error"] else []
    
    # Extract and classify metrics
    classified_metrics = extract_metrics_from_summary(summary, thresholds)
    
    # Extract pods
    detailed_pods = extract_detailed_pods(metrics_data)
    namespaces = list(set(p.get("namespace", "default") for p in detailed_pods)) or ["default"]
    
    output = {
        "sentinel": {
            "summary": summary,
            "metrics": classified_metrics,
        },
        "kubernetes": {
            "pods": detailed_pods,
            "namespaces": namespaces,
            "pod_count": summary.get("pod_count", 0),
            "running_pods": summary.get("running_pods", 0),
        },
        # Backward compatibility: keep "prometheus" key for benchmark.py
        "prometheus": {"metrics": classified_metrics},
        "thresholds_source": "config/thresholds.yaml",
        "data_source": "sentinel-go-agent",
    }

    print(json.dumps(output, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
