#!/usr/bin/env python3
"""
benchmark.py — Ciclo autônomo de benchmark com telemetria FDR
Sentinel - Claude Code Edition

Executa o pipeline completo (coleta → correlação → relatório),
mede o tempo de cada fase e grava um FDR em reports/.

Dados coletados via Sentinel Go agent (standalone, sem Prometheus).

Thresholds lidos de config/thresholds.yaml — source of truth único.

Uso:
    python3 tools/benchmark.py
"""

import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

import yaml

BASE_DIR = os.path.dirname(os.path.abspath(__file__))
THRESHOLDS_FILE = os.path.join(BASE_DIR, "..", "config", "thresholds.yaml")
REPORT_DIR = os.path.join(BASE_DIR, "..", "reports")
MONITOR_SCRIPT = os.path.join(BASE_DIR, "monitor.py")
REPORT_SCRIPT = os.path.join(BASE_DIR, "report_tool.py")


def load_thresholds() -> dict:
    with open(THRESHOLDS_FILE, encoding="utf-8") as f:
        return yaml.safe_load(f)


def run_monitor() -> dict:
    result = subprocess.run(
        [sys.executable, MONITOR_SCRIPT],
        capture_output=True,
        text=True,
        timeout=30,
    )
    if result.returncode != 0:
        raise RuntimeError(f"monitor.py falhou: {result.stderr.strip()}")
    return json.loads(result.stdout)


def correlate(data: dict, thresholds: dict) -> tuple[str, list, list]:
    """
    Ambiente dev/Minikube não-persistente: restarts acumulados são ruído normal
    (cluster desligado/ligado entre sessões). Severidade é baseada exclusivamente
    no estado atual do pod e nas métricas de recursos.
    """
    # Support both old "prometheus" key and new "sentinel" key
    metrics = data.get("prometheus", {}).get("metrics", {})
    if not metrics:
        metrics = data.get("sentinel", {}).get("metrics", {})
    
    pods = data.get("kubernetes", {}).get("pods", [])
    issues = []
    info = []  # contexto informativo — não eleva severidade
    severity = "OK"

    def escalate(new_sev):
        nonlocal severity
        order = ["OK", "WARNING", "CRITICAL"]
        if order.index(new_sev) > order.index(severity):
            severity = new_sev

    # Métricas de recursos
    for metric_name, m in metrics.items():
        if m.get("status") in ("WARNING", "CRITICAL"):
            issues.append(f"{metric_name}: {m['value']}% [{m['status']}]")
            escalate(m["status"])

    # Estado atual dos pods — restarts NÃO contribuem para severidade
    active_bad_states = {
        "CrashLoopBackOff": "CRITICAL",
        "OOMKilled": "CRITICAL",
        "Error": "WARNING",
        "Evicted": "WARNING",
        "Unknown": "WARNING",
    }
    for pod in pods:
        if "error" in pod or pod.get("summary"):
            continue
        reason = pod.get("waiting_reason") or pod.get("phase", "")
        if reason in active_bad_states:
            sev = active_bad_states[reason]
            issues.append(f"{pod['namespace']}/{pod['name']}: {reason} [{sev}]")
            escalate(sev)
        elif pod.get("restarts", 0) > 0:
            # Restarts: apenas contexto, não severidade
            info.append(
                f"{pod['namespace']}/{pod['name']}: {pod['restarts']} restarts "
                f"(cumulativo — ambiente dev)"
            )

    return severity, issues, info


def save_benchmark_report(stats: dict) -> str:
    ts = stats["ts_inicio"].strftime("%Y-%m-%d_%H-%M")
    filename = os.path.join(REPORT_DIR, f"benchmark_{ts}.md")

    issues_md = "\n".join(f"- {i}" for i in stats["issues"]) or "- Nenhuma anomalia detectada"
    info_md = "\n".join(f"- {i}" for i in stats["info"]) or "- N/A"
    namespaces_str = ", ".join(stats["namespaces"])

    content = f"""# Benchmark — Sentinel - Claude Code Edition

## Resumo Executivo

Ciclo autônomo de benchmark executado em {stats['duration_total']:.1f}s. \
Severidade detectada: **{stats['severity']}**.

**Data/Hora de início:** {stats['ts_inicio'].strftime('%Y-%m-%dT%H:%M:%SZ')}
**Data/Hora de fim:** {stats['ts_fim'].strftime('%Y-%m-%dT%H:%M:%SZ')}
**Duração total:** {stats['duration_total']:.1f}s
**Fonte de dados:** Sentinel Go Agent (standalone)

## Tempos por Fase

| Fase              | Duração (s) |
|-------------------|-------------|
| Sanitização       | {stats['duration_sanitize']:.2f}s |
| Coleta paralela   | {stats['duration_collect']:.2f}s |
| Correlação        | {stats['duration_correlate']:.2f}s |
| Relatório         | {stats['duration_report']:.2f}s |
| **Total**         | **{stats['duration_total']:.1f}s** |

## Contadores de Execução

| Métrica                    | Valor |
|----------------------------|-------|
| Chamadas Python realizadas | {stats['python_calls']} |
| Namespaces analisados      | {len(stats['namespaces'])} |
| Pods coletados             | {stats['pod_count']} |

## Métricas Coletadas

| Métrica  | Valor   | Status |
|----------|---------|--------|
| CPU      | {stats['cpu']} | {stats['cpu_status']} |
| Memória  | {stats['memory']} | {stats['memory_status']} |
| Disco    | {stats['disk']} | {stats['disk_status']} |

## Anomalias Detectadas

{issues_md}

## Contexto Informativo (não eleva severidade)

> Ambiente dev/Minikube: restarts abaixo são cumulativos de múltiplos ciclos de boot — esperados e normais.

{info_md}

## Contexto

- **Severidade detectada:** {stats['severity']}
- **Namespaces verificados:** {namespaces_str}
- **Thresholds lidos de:** config/thresholds.yaml
- **Fonte de dados:** Sentinel Go Agent (não depende de Prometheus)
"""

    os.makedirs(REPORT_DIR, exist_ok=True)
    with open(filename, "w", encoding="utf-8") as f:
        f.write(content)

    return filename


def fmt_metric(m: dict) -> tuple[str, str]:
    if m.get("value") is None:
        return "N/A", m.get("status", "UNKNOWN")
    return f"{m['value']}%", m.get("status", "UNKNOWN")


def main():
    thresholds = load_thresholds()
    stats = {"python_calls": 0}

    # FASE 0 — Registro de início
    stats["ts_inicio"] = datetime.now(timezone.utc)
    print(f"[BENCHMARK] Início: {stats['ts_inicio'].strftime('%Y-%m-%dT%H:%M:%SZ')}")

    # FASE 1 — Sanitização
    print("[BENCHMARK] Fase 1/4 — Sanitização...")
    t0 = time.time()
    for f in os.listdir("."):
        if f.endswith(".json"):
            os.remove(f)
    stats["duration_sanitize"] = time.time() - t0
    stats["python_calls"] += 1
    print(f"[BENCHMARK] Sanitização concluída em {stats['duration_sanitize']:.2f}s")

    # FASE 2 — Coleta
    print("[BENCHMARK] Fase 2/4 — Coleta via Sentinel Go Agent...")
    t0 = time.time()
    try:
        data = run_monitor()
        stats["python_calls"] += 1
    except Exception as exc:
        print(f"[BENCHMARK] FAILED na coleta: {exc}")
        print("[BENCHMARK] Dica: O Go agent está rodando? Inicie com: cd agent && make start")
        sys.exit(1)
    stats["duration_collect"] = time.time() - t0

    # Support both old and new data structure
    metrics = data.get("prometheus", {}).get("metrics", {})
    if not metrics:
        metrics = data.get("sentinel", {}).get("metrics", {})
    
    pods = data.get("kubernetes", {}).get("pods", [])
    stats["namespaces"] = data.get("kubernetes", {}).get("namespaces", [])
    stats["pod_count"] = len([p for p in pods if "error" not in p and not p.get("summary")])

    cpu_val, cpu_st = fmt_metric(metrics.get("cpu_usage_percent", {}))
    mem_val, mem_st = fmt_metric(metrics.get("memory_usage_percent", {}))
    dsk_val, dsk_st = fmt_metric(metrics.get("disk_usage_percent", {}))
    stats.update({"cpu": cpu_val, "cpu_status": cpu_st,
                  "memory": mem_val, "memory_status": mem_st,
                  "disk": dsk_val, "disk_status": dsk_st})

    print(f"[BENCHMARK] Coleta concluída em {stats['duration_collect']:.2f}s — "
          f"CPU:{cpu_val} Mem:{mem_val} Disco:{dsk_val}")

    # FASE 3 — Correlação
    print("[BENCHMARK] Fase 3/4 — Correlação...")
    t0 = time.time()
    severity, issues, info = correlate(data, thresholds)
    stats["python_calls"] += 1
    stats["duration_correlate"] = time.time() - t0
    stats["severity"] = severity
    stats["issues"] = issues
    stats["info"] = info
    print(f"[BENCHMARK] Correlação concluída em {stats['duration_correlate']:.2f}s — "
          f"severidade: {severity}")

    # FASE 4 — Relatório
    print("[BENCHMARK] Fase 4/4 — Gravando relatório...")
    t0 = time.time()
    stats["ts_fim"] = datetime.now(timezone.utc)
    stats["duration_total"] = (stats["ts_fim"] - stats["ts_inicio"]).total_seconds()
    stats["duration_report"] = 0.0  # calculado abaixo

    report_file = save_benchmark_report(stats)
    stats["python_calls"] += 1
    stats["duration_report"] = time.time() - t0
    print(f"[BENCHMARK] Relatório gravado em {stats['duration_report']:.2f}s → {report_file}")

    # FASE 5 — Box visual
    dm = int(stats["duration_total"] // 60)
    ds = int(stats["duration_total"] % 60)
    print(f"""
╔══════════════════════════════════════════════════════════╗
║         Sentinel - Claude Code Edition — Benchmark       ║
╚══════════════════════════════════════════════════════════╝
  Tempo total:        {stats['duration_total']:.1f}s ({dm}min {ds}s)
  Fase sanitização:   {stats['duration_sanitize']:.2f}s
  Fase coleta:        {stats['duration_collect']:.2f}s
  Fase correlação:    {stats['duration_correlate']:.2f}s
  Fase relatório:     {stats['duration_report']:.2f}s
  Python calls:       {stats['python_calls']}
  Namespaces:         {len(stats['namespaces'])} analisados
  Severidade:         {stats['severity']}
  Fonte de dados:     Sentinel Go Agent
  Relatório salvo:    {report_file}
""")


if __name__ == "__main__":
    main()
