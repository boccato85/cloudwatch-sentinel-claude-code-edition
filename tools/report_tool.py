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
from datetime import datetime, timezone


HARNESS = os.path.join(os.path.dirname(__file__), "..", "harness", "validador_saida.py")
REPORT_DIR = os.path.join(os.path.dirname(__file__), "..", "reports")
RUNBOOK_DIR = os.path.join(os.path.dirname(__file__), "..", "runbooks")


def validate_and_write(content: str, filepath: str) -> dict:
    """Passa o conteúdo pelo validador antes de gravar."""
    try:
        result = subprocess.run(
            [sys.executable, HARNESS],
            input=content,
            capture_output=True,
            text=True,
            timeout=10,
        )
    except FileNotFoundError:
        return {"status": "error", "message": f"Harness não encontrado: {HARNESS}", "file": None}
    except subprocess.TimeoutExpired:
        return {"status": "error", "message": "Timeout na validação do harness (>10s)", "file": None}

    if result.returncode != 0:
        return {
            "status": "error",
            "message": f"Validador bloqueou a gravação: {result.stderr.strip()}",
            "file": None,
        }

    os.makedirs(os.path.dirname(filepath), exist_ok=True, mode=0o700)
    fd = os.open(filepath, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(result.stdout)

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
