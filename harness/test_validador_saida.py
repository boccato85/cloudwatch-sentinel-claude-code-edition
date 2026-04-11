#!/usr/bin/env python3

import unittest

from validador_saida import validate


def valid_report_with(body: str) -> str:
    return (
        "## Resumo Executivo\n"
        "Este relatório atende ao tamanho mínimo e contém contexto suficiente.\n"
        f"{body}\n"
        "Texto adicional para manter o conteúdo acima de cem caracteres com detalhes operacionais."
    )


class TestValidadorSaida(unittest.TestCase):
    def test_blocks_casefolded_rm_rf_with_extra_spaces(self):
        errors = validate(valid_report_with("Comando: RM      -rF /tmp/abc"))
        self.assertTrue(any("rm -rf" in e.lower() for e in errors))

    def test_blocks_kubectl_delete_with_irregular_spacing(self):
        errors = validate(valid_report_with("Sugestão inválida: kubectl    delete pod x"))
        self.assertTrue(any("kubectl delete" in e.lower() for e in errors))

    def test_accepts_safe_content(self):
        errors = validate(valid_report_with("Ação recomendada: kubectl get pods e ajuste de requests."))
        self.assertEqual(errors, [])


if __name__ == "__main__":
    unittest.main()
