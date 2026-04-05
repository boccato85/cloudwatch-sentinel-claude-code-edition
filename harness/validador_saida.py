#!/usr/bin/env python3
"""
Harness Engineering — Output Validator
sentinel

Lê conteúdo do stdin, aplica regras de segurança e conformidade,
e escreve em stdout se válido. Encerra com exit(1) em caso de violação.

Uso:
    echo "<conteúdo>" | python3 harness/validador_saida.py > reports/relatorio.md
"""

import sys


FORBIDDEN_PATTERNS = [
    "rm -rf",
    "kubectl delete",
    "DROP TABLE",
    "DROP DATABASE",
    "TRUNCATE TABLE",
    "dd if=",
    "mkfs",
    "> /dev/",
    "format c:",
    ":(){:|:&};:",  # fork bomb
]

REQUIRED_SECTION = "## Resumo Executivo"
MIN_LENGTH = 100


def validate(text: str) -> list[str]:
    errors = []

    if len(text.strip()) < MIN_LENGTH:
        errors.append(
            f"Conteúdo muito curto ({len(text.strip())} chars). "
            f"Mínimo: {MIN_LENGTH} chars."
        )

    if REQUIRED_SECTION not in text:
        errors.append(
            f"Seção obrigatória ausente: '{REQUIRED_SECTION}'. "
            "Todo relatório deve conter esta seção."
        )

    for pattern in FORBIDDEN_PATTERNS:
        if pattern in text:
            errors.append(
                f"Comando destrutivo bloqueado: '{pattern}'. "
                "Remova o padrão antes de salvar."
            )

    return errors


def main():
    try:
        text = sys.stdin.read()
    except UnicodeDecodeError:
        sys.stderr.write("Erro: conteúdo não é UTF-8 válido.\n")
        sys.exit(1)

    errors = validate(text)

    if errors:
        sys.stderr.write("❌ Validador bloqueou a gravação:\n")
        for i, err in enumerate(errors, 1):
            sys.stderr.write(f"  {i}. {err}\n")
        sys.exit(1)

    sys.stdout.write(text)
    sys.exit(0)


if __name__ == "__main__":
    main()
