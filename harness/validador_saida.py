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
import re


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

FORBIDDEN_REGEX_PATTERNS = [
    (r"\brm\s+-rf\b", "rm -rf"),
    (r"\bkubectl\s+delete\b", "kubectl delete"),
    (r"\bdrop\s+table\b", "DROP TABLE"),
    (r"\bdrop\s+database\b", "DROP DATABASE"),
    (r"\btruncate\s+table\b", "TRUNCATE TABLE"),
    (r"\bdd\s+if\s*=", "dd if="),
    (r"\bmkfs\b", "mkfs"),
    (r">\s*/dev/", "> /dev/"),
    (r"\bformat\s+c:", "format c:"),
    (r":\(\)\{\s*:\|:&\s*\};:", ":(){:|:&};:"),
]

REQUIRED_SECTION = "## Resumo Executivo"
MIN_LENGTH = 100


def normalize_for_detection(text: str) -> str:
    lowered = text.casefold()
    return re.sub(r"\s+", " ", lowered).strip()


def validate(text: str) -> list[str]:
    errors = []
    normalized = normalize_for_detection(text)

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
    for regex, label in FORBIDDEN_REGEX_PATTERNS:
        if re.search(regex, normalized, flags=re.IGNORECASE):
            errors.append(
                f"Comando destrutivo bloqueado: '{label}'. "
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
