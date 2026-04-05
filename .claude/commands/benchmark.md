# benchmark

Você é o executor de benchmark do CloudWatch Sentinel - Claude Code Edition. Seu objetivo é medir com precisão o tempo de cada fase do `/sentinel` e gerar um relatório consolidado de desempenho.

## Variáveis de controle

Antes de iniciar, inicialize as seguintes variáveis mentais que você vai atualizar ao longo da execução:

- `ts_inicio` — timestamp ISO 8601 do início geral
- `ts_fim_startup` — timestamp ao fim da fase de startup
- `ts_fim_coleta` — timestamp ao fim da fase de coleta paralela
- `ts_fim_correlacao` — timestamp ao fim da fase de correlação
- `ts_fim_relatorio` — timestamp ao fim da fase de geração de relatório/runbook
- `ts_fim` — timestamp ISO 8601 do fim geral
- `bash_calls` — contador de chamadas bash realizadas durante todo o ciclo
- `subagents_disparados` — contador de sub-agents invocados
- `namespaces_analisados` — lista dos namespaces verificados
- `severidade_detectada` — resultado final do correlacionador

---

## Fluxo de execução

### FASE 0 — Registro de início

Registre `ts_inicio` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Exiba: `[BENCHMARK] Início: <ts_inicio>`

Incremente `bash_calls` em 1.

---

### FASE 1 — Startup

Exiba: `[BENCHMARK] Fase 1/4 — Startup iniciando...`

Execute `/startup` normalmente.

Ao concluir, registre `ts_fim_startup` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1. Incremente `subagents_disparados` em 1.

Calcule `duracao_startup` = `ts_fim_startup` − `ts_inicio` (em segundos).

Exiba: `[BENCHMARK] Fase startup concluída em <duracao_startup>s`

- Se o `/startup` retornar FAILED: encerre o benchmark, exiba o erro e não continue.

---

### FASE 2 — Coleta paralela

Exiba: `[BENCHMARK] Fase 2/4 — Coleta paralela iniciando...`

Registre `ts_inicio_coleta` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Dispare simultaneamente:
- Sub-agent A: `/collect-metrics`
- Sub-agent B: `/analyze-pods`

Incremente `subagents_disparados` em 2.

Ao concluir ambos, registre `ts_fim_coleta` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Calcule `duracao_coleta` = `ts_fim_coleta` − `ts_inicio_coleta` (em segundos).

Identifique os namespaces que foram verificados pelo `/analyze-pods` e preencha `namespaces_analisados`.

Exiba: `[BENCHMARK] Fase coleta concluída em <duracao_coleta>s — namespaces: <namespaces_analisados>`

---

### FASE 3 — Correlação

Exiba: `[BENCHMARK] Fase 3/4 — Correlação iniciando...`

Registre `ts_inicio_correlacao` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Execute `/correlate` com os outputs das fases anteriores.

Incremente `subagents_disparados` em 1.

Ao concluir, registre `ts_fim_correlacao` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Calcule `duracao_correlacao` = `ts_fim_correlacao` − `ts_inicio_correlacao` (em segundos).

Preencha `severidade_detectada` com o resultado retornado pelo `/correlate` (CRITICAL | WARNING | OK).

Exiba: `[BENCHMARK] Fase correlação concluída em <duracao_correlacao>s — severidade: <severidade_detectada>`

---

### FASE 4 — Geração de relatório ou runbook

Exiba: `[BENCHMARK] Fase 4/4 — Geração de relatório iniciando...`

Registre `ts_inicio_relatorio` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Execute a ação correspondente à severidade detectada, exatamente como definido no `/sentinel`:

- **CRITICAL:** gere e salve o runbook em `./runbooks/`
- **WARNING:** gere e salve o relatório em `./reports/`
- **OK:** salve o status report em `./reports/`

Ao concluir, registre `ts_fim_relatorio` e `ts_fim` executando:

```bash
date -u +"%Y-%m-%dT%H:%M:%SZ"
```

Incremente `bash_calls` em 1.

Calcule `duracao_relatorio` = `ts_fim_relatorio` − `ts_inicio_relatorio` (em segundos).

---

### FASE 5 — Cálculo e exibição do benchmark

Com todos os timestamps registrados, calcule:

- `duracao_total_segundos` = `ts_fim` − `ts_inicio` (em segundos inteiros)
- `duracao_total_minutos` = parte inteira de `duracao_total_segundos / 60`
- `duracao_total_resto_segundos` = `duracao_total_segundos mod 60`

Determine o nome do arquivo de benchmark:

```bash
date -u +"%Y-%m-%d_%H-%M"
```

Use o resultado para formar: `./reports/benchmark_<data>.md`

Incremente `bash_calls` em 1.

Exiba o box visual exatamente neste formato (substitua os valores reais):

```
╔══════════════════════════════════════════════════════════╗
║   CloudWatch Sentinel - Claude Code Edition - Claude Code Edition — Benchmark  ║
╚══════════════════════════════════════════════════════════╝
  Tempo total:        <duracao_total_segundos>s (<duracao_total_minutos>min <duracao_total_resto_segundos>s)
  Fase startup:       <duracao_startup>s
  Fase coleta:        <duracao_coleta>s
  Fase correlação:    <duracao_correlacao>s
  Fase relatório:     <duracao_relatorio>s
  Sub-agents:         <subagents_disparados> disparados
  Namespaces:         <count(namespaces_analisados)> analisados
  Severidade:         <severidade_detectada>
  Relatório salvo:    ./reports/benchmark_<data>.md
```

---

### FASE 6 — Salvar resultado do benchmark

Salve o arquivo `./reports/benchmark_<data>.md` com o seguinte conteúdo:

```markdown
# Benchmark — CloudWatch Sentinel - Claude Code Edition

**Data/Hora de início:** <ts_inicio>
**Data/Hora de fim:** <ts_fim>
**Duração total:** <duracao_total_segundos>s (<duracao_total_minutos>min <duracao_total_resto_segundos>s)

## Tempos por Fase

| Fase           | Duração (s) |
|----------------|-------------|
| Startup        | <duracao_startup>s |
| Coleta paralela| <duracao_coleta>s |
| Correlação     | <duracao_correlacao>s |
| Relatório      | <duracao_relatorio>s |
| **Total**      | **<duracao_total_segundos>s** |

## Contadores de Execução

| Métrica                  | Valor |
|--------------------------|-------|
| Chamadas bash realizadas | <bash_calls> |
| Sub-agents disparados    | <subagents_disparados> |
| Namespaces analisados    | <count(namespaces_analisados)> |

## Contexto

- **Severidade detectada:** <severidade_detectada>
- **Namespaces verificados:** <namespaces_analisados (lista separada por vírgula)>
- **Timestamps registrados:**
  - Início geral: `<ts_inicio>`
  - Fim startup: `<ts_fim_startup>`
  - Fim coleta: `<ts_fim_coleta>`
  - Fim correlação: `<ts_fim_correlacao>`
  - Fim relatório/runbook: `<ts_fim_relatorio>`
  - Fim geral: `<ts_fim>`
```

---

## Princípios

- Não interrompa o fluxo normal do `/sentinel` — o benchmark apenas envolve e mede a execução
- Registre os timestamps com chamadas bash reais (`date -u`), não estimativas
- Nunca execute ações destrutivas no cluster sem confirmação explícita do usuário
- Se qualquer fase falhar, registre o ponto de falha no relatório antes de encerrar
