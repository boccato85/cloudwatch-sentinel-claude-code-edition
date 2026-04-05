#!/usr/bin/env python3
"""
monitor.py — Coleta paralela de métricas
CloudWatch Sentinel - Claude Code Edition

Coleta em paralelo:
  - Status de pods via kubernetes-client (default, monitoring, kube-system)
  - Métricas de CPU / Memória / Disco via Prometheus HTTP API

Saída: JSON em stdout para consumo pelo agente.

Uso:
    python3 tools/monitor.py
"""

import json
import os
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

import requests
import yaml
from kubernetes import client, config as k8s_config

PROMETHEUS_URL = os.environ.get("PROMETHEUS_URL", "http://localhost:9090")
NAMESPACES = ["default", "monitoring", "kube-system"]
THRESHOLDS_FILE = os.path.join(os.path.dirname(__file__), "..", "config", "thresholds.yaml")


def load_thresholds() -> dict:
    with open(THRESHOLDS_FILE, encoding="utf-8") as f:
        return yaml.safe_load(f)


def get_prometheus_metric(query: str, name: str) -> dict:
    try:
        resp = requests.get(
            f"{PROMETHEUS_URL}/api/v1/query",
            params={"query": query},
            timeout=5,
        )
        resp.raise_for_status()
        result = resp.json().get("data", {}).get("result", [])
        value = round(float(result[0]["value"][1]), 2) if result else None
        return {"name": name, "value": value, "error": None}
    except Exception as exc:
        return {"name": name, "value": None, "error": str(exc)}


def get_metrics() -> dict:
    queries = {
        "cpu_usage_percent": (
            '100 - (avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100)'
        ),
        "memory_usage_percent": (
            "100 - (node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes * 100)"
        ),
        "disk_usage_percent": (
            '100 - (node_filesystem_avail_bytes{mountpoint="/"} '
            '/ node_filesystem_size_bytes{mountpoint="/"} * 100)'
        ),
    }

    results = {}
    with ThreadPoolExecutor(max_workers=3) as pool:
        futures = {
            pool.submit(get_prometheus_metric, q, name): name
            for name, q in queries.items()
        }
        for future in as_completed(futures):
            data = future.result()
            results[data["name"]] = {
                "value": data["value"],
                "error": data["error"],
            }
    return results


def classify_metric(value, warning_threshold, critical_threshold) -> str:
    if value is None:
        return "UNKNOWN"
    if value > critical_threshold:
        return "CRITICAL"
    if value > warning_threshold:
        return "WARNING"
    return "OK"


def get_pod_status() -> list:
    try:
        k8s_config.load_kube_config()
        v1 = client.CoreV1Api()
        pods = []
        for ns in NAMESPACES:
            pod_list = v1.list_namespaced_pod(ns)
            for pod in pod_list.items:
                restarts = sum(
                    (cs.restart_count or 0)
                    for cs in (pod.status.container_statuses or [])
                )
                waiting_reason = None
                for cs in pod.status.container_statuses or []:
                    if cs.state and cs.state.waiting:
                        waiting_reason = cs.state.waiting.reason
                        break
                pods.append({
                    "namespace": ns,
                    "name": pod.metadata.name,
                    "phase": pod.status.phase,
                    "restarts": restarts,
                    "waiting_reason": waiting_reason,
                })
        return pods
    except Exception as exc:
        return [{"error": str(exc)}]


def main():
    thresholds = load_thresholds()

    with ThreadPoolExecutor(max_workers=2) as pool:
        future_pods = pool.submit(get_pod_status)
        future_metrics = pool.submit(get_metrics)

    pods = future_pods.result()
    metrics = future_metrics.result()

    classified = {}
    for metric_name, data in metrics.items():
        key = metric_name.replace("_usage_percent", "").replace("_percent", "")
        th = thresholds.get(key, {})
        classified[metric_name] = {
            **data,
            "status": classify_metric(
                data["value"],
                th.get("warning", 70),
                th.get("critical", 85),
            ),
        }

    output = {
        "kubernetes": {"pods": pods, "namespaces": NAMESPACES},
        "prometheus": {"metrics": classified},
        "thresholds_source": "config/thresholds.yaml",
    }

    print(json.dumps(output, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
