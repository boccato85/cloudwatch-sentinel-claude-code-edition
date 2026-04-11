#!/usr/bin/env python3
"""
report_tool.py — Geração e gravação segura de relatórios
sentinel

Todo relatório passa obrigatoriamente pelo harness/validador_saida.py
antes de ser gravado em disco.

Uso:
    python3 tools/report_tool.py --severity WARNING --content '<markdown>'
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
from datetime import datetime, timezone


HARNESS = os.path.join(os.path.dirname(__file__), "..", "harness", "validador_saida.py")
REPORT_DIR = os.path.join(os.path.dirname(__file__), "..", "reports")
RUNBOOK_DIR = os.path.join(os.path.dirname(__file__), "..", "runbooks")


def validate_and_write(content: str, filepath: str) -> dict:
    """Passa o conteúdo pelo validador antes de gravar."""
    timeout_sec = int(os.getenv("HARNESS_TIMEOUT_SEC", "10"))
    if timeout_sec <= 0:
        timeout_sec = 10

    try:
        result = subprocess.run(
            [sys.executable, HARNESS],
            input=content,
            capture_output=True,
            text=True,
            timeout=timeout_sec,
        )
    except FileNotFoundError:
        return {"status": "error", "message": f"Harness não encontrado: {HARNESS}", "file": None}
    except subprocess.TimeoutExpired:
        return {"status": "error", "message": f"Timeout na validação do harness (>{timeout_sec}s)", "file": None}

    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "sem detalhes adicionais"
        return {
            "status": "error",
            "message": f"Validador bloqueou a gravação (exit={result.returncode}): {detail}",
            "file": None,
        }

    os.makedirs(os.path.dirname(filepath), exist_ok=True, mode=0o700)
    tmp_path = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            delete=False,
            dir=os.path.dirname(filepath),
            prefix=".tmp_report_",
        ) as tmp:
            tmp.write(result.stdout)
            tmp.flush()
            os.fsync(tmp.fileno())
            tmp_path = tmp.name
        os.chmod(tmp_path, 0o600)
        os.replace(tmp_path, filepath)
    except OSError as exc:
        if tmp_path and os.path.exists(tmp_path):
            os.unlink(tmp_path)
        return {
            "status": "error",
            "message": f"Falha ao gravar relatório de forma atômica: {exc}",
            "file": None,
        }

    return {
        "status": "success",
        "message": "Relatório gravado com sucesso via harness.",
        "file": filepath,
    }


def main():
    parser = argparse.ArgumentParser(description="Gera relatório/runbook via harness.")
    parser.add_argument(
        "--severity",
        required=True,
        choices=["OK", "WARNING", "CRITICAL"],
        help="Severidade detectada pelo correlacionador.",
    )
    parser.add_argument(
        "--content",
        required=True,
        help="Conteúdo Markdown do relatório.",
    )
    parser.add_argument(
        "--component",
        default="k8s",
        help="Componente afetado (usado no nome do runbook CRITICAL).",
    )
    args = parser.parse_args()

    ts = datetime.now(timezone.utc).strftime("%Y-%m-%d_%H-%M-%S")

    if args.severity == "CRITICAL":
        filepath = os.path.join(RUNBOOK_DIR, f"{ts}_CRITICAL_{args.component}.md")
    else:
        filepath = os.path.join(REPORT_DIR, f"{ts}_{args.severity}.md")

    result = validate_and_write(args.content, filepath)
    print(json.dumps(result, indent=2, ensure_ascii=False))

    if result["status"] == "error":
        sys.exit(1)


if __name__ == "__main__":
    main()
