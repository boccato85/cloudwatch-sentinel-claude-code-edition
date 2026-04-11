#!/usr/bin/env python3

import unittest

from validador_saida import validate, normalize_unicode, MAX_INPUT_SIZE


def valid_report_with(body: str) -> str:
    return (
        "## Resumo Executivo\n"
        "Este relatório atende ao tamanho mínimo e contém contexto suficiente.\n"
        f"{body}\n"
        "Texto adicional para manter o conteúdo acima de cem caracteres com detalhes operacionais."
    )


class TestValidadorSaida(unittest.TestCase):
    # ===== Existing tests =====
    def test_blocks_casefolded_rm_rf_with_extra_spaces(self):
        errors = validate(valid_report_with("Comando: RM      -rF /tmp/abc"))
        self.assertTrue(any("rm -rf" in e.lower() for e in errors))

    def test_blocks_kubectl_delete_with_irregular_spacing(self):
        errors = validate(valid_report_with("Sugestão inválida: kubectl    delete pod x"))
        self.assertTrue(any("kubectl delete" in e.lower() for e in errors))

    def test_accepts_safe_content(self):
        errors = validate(valid_report_with("Ação recomendada: kubectl get pods e ajuste de requests."))
        self.assertEqual(errors, [])

    # ===== P3: Expanded test coverage =====
    def test_blocks_drop_table(self):
        errors = validate(valid_report_with("Execute: DROP TABLE users;"))
        self.assertTrue(any("drop table" in e.lower() for e in errors))

    def test_blocks_drop_database(self):
        errors = validate(valid_report_with("Comando: drop   database production;"))
        self.assertTrue(any("drop database" in e.lower() for e in errors))

    def test_blocks_truncate_table(self):
        errors = validate(valid_report_with("TRUNCATE  TABLE logs;"))
        self.assertTrue(any("truncate table" in e.lower() for e in errors))

    def test_blocks_dd_if(self):
        errors = validate(valid_report_with("dd  if=/dev/zero of=/dev/sda"))
        self.assertTrue(any("dd if=" in e.lower() for e in errors))

    def test_blocks_mkfs(self):
        errors = validate(valid_report_with("Run: mkfs.ext4 /dev/sda1"))
        self.assertTrue(any("mkfs" in e.lower() for e in errors))

    def test_blocks_fork_bomb(self):
        errors = validate(valid_report_with(":(){:|:&};:"))
        self.assertTrue(any(":(){" in e or "fork" in e.lower() for e in errors))

    def test_blocks_dev_redirect(self):
        errors = validate(valid_report_with("echo test > /dev/sda"))
        self.assertTrue(any("> /dev/" in e for e in errors))

    def test_requires_resumo_executivo(self):
        text = "Texto sem a seção obrigatória mas com tamanho suficiente para validação geral do sistema."
        errors = validate(text)
        self.assertTrue(any("Resumo Executivo" in e for e in errors))

    def test_rejects_short_content(self):
        errors = validate("## Resumo Executivo\nCurto")
        self.assertTrue(any("muito curto" in e.lower() for e in errors))

    def test_rejects_empty_content(self):
        errors = validate("")
        self.assertTrue(len(errors) > 0)

    def test_rejects_oversized_content(self):
        huge = "## Resumo Executivo\n" + "x" * (MAX_INPUT_SIZE + 100)
        errors = validate(huge)
        self.assertTrue(any("limite máximo" in e.lower() for e in errors))

    # ===== Unicode normalization tests =====
    def test_normalize_unicode_removes_invisible_chars(self):
        # Zero-width space (U+200B) should be removed
        text_with_invisible = "rm\u200b -rf"
        normalized = normalize_unicode(text_with_invisible)
        self.assertNotIn("\u200b", normalized)

    def test_normalize_unicode_nfkc(self):
        # Fullwidth characters should be normalized
        fullwidth_rm = "ｒｍ"  # fullwidth 'rm'
        normalized = normalize_unicode(fullwidth_rm)
        self.assertEqual(normalized, "rm")


if __name__ == "__main__":
    unittest.main()
