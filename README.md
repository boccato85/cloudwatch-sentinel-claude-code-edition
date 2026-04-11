# 🛡️ Sentinel

<p align="center">
  <img src="cw_sentinel_logo.png" alt="Sentinel Logo" width="180"/>
</p>

> Plataforma minimalista de observabilidade e FinOps para clusters Kubernetes — dashboard em tempo real, análise de incidentes com LLM e rastreamento de custo por pod.

<p align="center">
  <img src="cw_sentinel_ss.png" alt="Sentinel Dashboard" width="900"/>
</p>

![Status](https://img.shields.io/badge/status-v1.7-brightgreen)
![Claude Code](https://img.shields.io/badge/Claude%20Code-native-orange)
![Kubernetes](https://img.shields.io/badge/Kubernetes-v1.35.1-blue)
![Go](https://img.shields.io/badge/Go-agent-00ADD8)
![Standalone](https://img.shields.io/badge/standalone-no%20Prometheus-green)
![License](https://img.shields.io/badge/license-Apache%202.0-blue)

---

## O que é

Sentinel evoluiu de um agente de monitoramento reativo para uma plataforma completa de observabilidade e FinOps. A arquitetura combina duas camadas complementares:

- **Go Agent** — dashboard web proativo em tempo real (porta 8080), coleta contínua de métricas e histórico de custo por pod persistido no PostgreSQL
- **Claude Code** — análise de incidentes sob demanda com raciocínio LLM, geração de runbooks e recomendações de remediação

O projeto demonstra na prática:
- **Go agent** com Kubernetes client-go para coleta autônoma de métricas
- **FinOps** — rastreamento de waste (recursos alocados vs utilizados) e histórico de custo por pod
- **Standalone** — não depende de Prometheus/Grafana/AlertManager
- **MCP Server** para integração com kubectl
- **CLAUDE.md** como contexto operacional persistente
- **Slash commands** como interface de resposta a incidentes

---

## Arquitetura

```
┌─────────────────────────────────────────────────────┐
│                   Go Agent (porta 8080)             │
│  coleta contínua → PostgreSQL → dashboard em tempo  │
│  real com custo por pod e histórico configurável    │
└───────────────────────┬─────────────────────────────┘
                        │ /api/summary /api/metrics /api/history
                        ▼
┌─────────────────────────────────────────────────────┐
│                  Claude Code                        │
│                                                     │
│  /startup                                           │
│    └─ Minikube + Go agent                           │
│                                                     │
│  /incident                                          │
│    └─ consome API do Go agent                       │
│    └─ raciocínio LLM + análise FinOps               │
│    └─ classifica severidade                         │
│    └─ gera runbook/relatório via harness            │
└─────────────────────────────────────────────────────┘
```

---

## Stack

| Camada | Tecnologia |
|---|---|
| Cluster | Minikube (KVM2) — Kubernetes v1.35.1 |
| Dashboard | Go agent (Kubernetes client-go + net/http) |
| Persistência | PostgreSQL (`sentinel_db`) |
| Agente LLM | Claude Code |
| Integrações | MCP Server kubectl |
| Output | Runbooks e relatórios em Markdown (validados pelo harness) |

---

## Pré-requisitos

- [Claude Code](https://claude.ai/code) instalado e autenticado
- Minikube rodando
- Go 1.23+
- PostgreSQL local com database `sentinel_db`
- Node.js (para o MCP Server via npx)

---

## Setup

### 1. PostgreSQL

```bash
createdb sentinel_db
```

Variáveis de ambiente opcionais (defaults: `postgres` / `postgres` / `localhost`):

```bash
export DB_USER=postgres
export DB_PASSWORD=postgres
export DB_NAME=sentinel_db
export DB_HOST=localhost
export DB_SSLMODE=disable
export DB_TIMEOUT_SEC=5
```

### 2. Clone e MCP Server

```bash
git clone https://github.com/boccato85/Sentinel
cd sentinel

claude mcp add kubectl -- npx -y kubectl-mcp-server
```

### 3. Go Agent

**Opção A: Standalone (desenvolvimento local)**

```bash
cd agent
make build   # compila o binário
make start   # inicia o serviço (ou use /startup que faz isso automaticamente)
```

**Opção B: Deploy no Kubernetes via Helm**

```bash
# Build da imagem no Minikube
cd agent
minikube image build -t sentinel:v1.6 .

# Deploy no namespace sentinel
helm install sentinel helm/sentinel -n sentinel --create-namespace \
  --set image.tag=v1.6 \
  --set image.pullPolicy=Never

# Verificar status
kubectl get pods -n sentinel

# Acessar o dashboard
kubectl port-forward -n sentinel svc/sentinel 8080:8080
```

O chart inclui:
- Deployment do Go agent com ServiceAccount e RBAC
- PostgreSQL interno (ou externo via `postgresql.external=true`)
- ConfigMap e Secret para configuração
- Service ClusterIP na porta 8080

---

## Uso

```bash
claude
```

**Bootstrap do ambiente:**
```
/startup
```
Verifica Minikube e inicia o Go agent se estiver down. Output:

```
╔═══════════════════════════════════════════════════════════╗
║                    Sentinel — Startup                     ║
╚═══════════════════════════════════════════════════════════╝

 Minikube      (cluster)          →  ✅ OK
 Go Agent      (localhost:8080)   →  ✅ STARTED
```

**Análise de incidente:**
```
/incident
```
Consome os dados do Go agent, aplica raciocínio LLM com análise FinOps e gera o relatório ou runbook automaticamente.

---

## Go Agent — Dashboard

Após o `/startup`, acesse `http://localhost:8080`.

| Endpoint | Descrição |
|---|---|
| `GET /` | Dashboard Dynatrace-style (HTML) |
| `GET /api/summary` | Estado do cluster: nodes, pods, CPU |
| `GET /api/metrics` | Métricas por pod: CPU usage, waste (`cpuRequestPresent`, `potentialSavingMCpu`) |
| `GET /api/history` | Histórico de custo (ver ranges abaixo) |
| `GET /api/history?range=30m` | Últimos 30 minutos (default) |
| `GET /api/history?range=24h` | Últimas 24 horas |
| `GET /api/history?range=7d` | Últimos 7 dias |
| `GET /api/history?range=30d` | Últimos 30 dias |
| `GET /api/history?range=365d` | Último ano |

Gerenciamento manual:

```bash
cd agent/
make start    # compila + inicia o serviço em background
make stop     # para o serviço
make restart  # recompila e reinicia
make status   # estado atual
make logs     # tail dos logs em tempo real
```

---

## Outputs gerados pelo /incident

### Relatório WARNING / OK
```
reports/2026-04-05_14-30_WARNING.md
```
Contém: estado do cluster, métricas no momento, análise de waste por pod, tendência de custo e recomendações com comandos kubectl prontos.

### Runbook CRITICAL
```
runbooks/2026-04-05_14-30_CRITICAL_prometheus.md
```
Contém: situação detectada, métricas no momento do incidente, análise FinOps, hipóteses de causa raiz e checklist de remediação.

---

## Thresholds

Definidos em `config/thresholds.yaml` — source of truth único, lido em runtime por todos os componentes.

| Métrica | WARNING | CRITICAL |
|---|---|---|
| CPU | > 70% | > 85% |
| Memória | > 75% | > 90% |
| Disco | > 70% | > 85% |
| Pod CrashLoopBackOff | — | imediato |
| Pod Pending > 5min | ✓ | — |
| Waste por pod | > 60% | — |

---

## Retenção de Dados

O Sentinel usa uma estratégia de retenção em camadas para balancear granularidade e uso de disco:

| Camada | Granularidade | Retenção Default | Configurável via |
|--------|---------------|------------------|------------------|
| Raw | 10 segundos | 24 horas | `RETENTION_RAW_HOURS` |
| Hourly | 1 hora | 30 dias | `RETENTION_HOURLY_DAYS` |
| Daily | 1 dia | 365 dias | `RETENTION_DAILY_DAYS` |

**Agregação automática:** A cada hora, o Sentinel:
1. Agrega métricas raw em buckets hourly
2. Agrega hourly em buckets daily
3. Remove dados antigos conforme a política de retenção

**Via Helm (recomendado para produção):**

```yaml
# values.yaml
retention:
  rawHours: 48       # 2 dias de dados raw
  hourlyDays: 90     # 3 meses de agregados hourly
  dailyDays: 730     # 2 anos de agregados daily
```

**Via variáveis de ambiente (standalone):**

```bash
export RETENTION_RAW_HOURS=24
export RETENTION_HOURLY_DAYS=30
export RETENTION_DAILY_DAYS=365
```

**Estimativa de uso de disco (por 100 pods):**

| Período | Raw | Hourly | Daily | Total |
|---------|-----|--------|-------|-------|
| 1 mês | ~50MB | ~15MB | ~1MB | ~66MB |
| 1 ano | N/A | ~180MB | ~12MB | ~192MB |

---

## Estrutura do projeto

```
sentinel/
├── CLAUDE.md                        # Contexto operacional do agente
├── README.md
├── .mcp.json                        # Configuração dos MCP Servers
├── .gitignore
├── agent/
│   ├── main.go                      # Go agent: dashboard + coleta + PostgreSQL
│   ├── Dockerfile                   # Build multi-stage para Kubernetes
│   ├── go.mod / go.sum
│   └── Makefile                     # build, start, stop, restart, status, logs
├── helm/
│   └── sentinel/                    # Helm chart para deploy no Kubernetes
│       ├── Chart.yaml
│       ├── values.yaml
│       └── templates/
├── config/
│   └── thresholds.yaml              # Source of truth único de thresholds
├── tools/
│   ├── monitor.py                   # Coleta via Sentinel Go agent API
│   └── report_tool.py               # Gravação segura via harness
├── harness/
│   ├── validador_saida.py           # Gatekeeper: bloqueia destrutivos, exige Resumo Executivo
│   └── test_validador_saida.py      # Testes unitários (16 tests)
├── .claude/
│   └── commands/
│       ├── startup.md               # Bootstrap: Minikube + Go agent
│       └── incident.md              # Análise LLM + runbook via Go agent API
├── runbooks/                         # Runbooks CRITICAL gerados
└── reports/                         # Relatórios WARNING/OK gerados
```

---

## Harness Engineering

Todo relatório final passa pelo `harness/validador_saida.py` antes de ser gravado em disco. O validador aplica:

| Regra | Comportamento |
|---|---|
| Bloqueia comandos destrutivos | `rm -rf`, `kubectl delete`, `DROP TABLE`, `dd if=`, `mkfs`, fork bomb etc. |
| Exige `## Resumo Executivo` | Relatórios sem essa seção são rejeitados |
| Tamanho mínimo | Conteúdo menor que 100 caracteres é rejeitado |

Se qualquer regra for violada, o arquivo **não é criado**.

Variáveis úteis do fluxo de relatório:

```bash
export HARNESS_TIMEOUT_SEC=10
```

---

## Changelog

### v1.7
- **Standalone completo** — removida toda dependência de Prometheus/Grafana/AlertManager
- `tools/monitor.py` reescrito para usar API do Go agent (`/api/summary`, `/api/metrics`)
- `/startup` simplificado — apenas verifica Minikube e Go agent
- Removido MCP Server prometheus do `.mcp.json`

### v1.6
- **Retenção configurável** — política de retenção em 3 camadas (raw/hourly/daily) com cleanup automático
- **Histórico expandido** — `/api/history` agora suporta ranges de 30m até 365d
- **Agregação automática** — job que roda a cada hora compactando métricas antigas
- Novas tabelas: `metrics_hourly`, `metrics_daily`, `cost_history`

### v1.5
- **Helm chart** — deploy completo no Kubernetes com `helm install sentinel helm/sentinel -n sentinel`
- **InClusterConfig** — Go agent detecta automaticamente se está rodando dentro do cluster
- **Auto-schema** — tabela `metrics` criada automaticamente no startup
- **Security hardening** — connection pool PostgreSQL, rate limiting (100 rps), bind address configurável
- **Harness** — normalização Unicode (NFKC), limite de input (10MB), cobertura de testes expandida (16 tests)
- **Tools** — sanitização de `--component` contra path traversal, timeout com clamping seguro
- Adicionado `requirements.txt` para dependências Python
- Stack trace completo no panic recovery do Go agent

### v1.4
- **Go agent** (`agent/`) com dashboard web em tempo real na porta 8080
- **FinOps** — rastreamento de waste por pod e histórico de custo (últimos 30min) persistido no PostgreSQL
- **`/incident`** substitui `/sentinel` — análise LLM que consome diretamente a API do Go agent
- **`/startup`** passa a subir o Go agent automaticamente além dos port-forwards
- Removidos: `/sentinel`, `/collect-metrics`, `/analyze-pods`, `/correlate`, `/benchmark`

### v1.3
- Renomeado para **sentinel** — identidade minimalista em todos os arquivos

### v1.2
- `/startup`: Fase 0 — verifica `minikube status` antes de qualquer ação; se `Stopped`, executa `minikube start` com retry (20x, 15s)
- Renomeia o projeto para **CloudWatch Sentinel - Claude Code Edition**
- Adiciona `.mcp.json` com configuração dos MCP servers

### v1.1
- `/startup`: verifica e sobe port-forwards automaticamente
- Suporte a múltiplos namespaces (`default`, `monitoring`, `kube-system`)

### v1.0
- Release inicial: orquestrador + sub-agents paralelos (`/collect-metrics`, `/analyze-pods`, `/correlate`)
- Geração automática de runbooks CRITICAL e relatórios WARNING/OK

---

## Motivação

Projeto desenvolvido para explorar na prática a evolução de um agente Claude Code simples de monitoramento até uma plataforma de observabilidade e FinOps — combinando coleta autônoma via Go, persistência com PostgreSQL, dashboard em tempo real e raciocínio LLM para análise de incidentes.

---

## Licença

Distribuído sob a licença [Apache 2.0](LICENSE).
