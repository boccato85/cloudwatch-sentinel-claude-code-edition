# 🛡️ CloudWatch Sentinel

> Agente inteligente de monitoramento de clusters Kubernetes construído com Claude Code, sub-agents paralelos e MCP Servers.

![Status](https://img.shields.io/badge/status-active-brightgreen)
![Claude Code](https://img.shields.io/badge/Claude%20Code-2.1.76-orange)
![Kubernetes](https://img.shields.io/badge/Kubernetes-v1.35.1-blue)
![Prometheus](https://img.shields.io/badge/Prometheus-kube--prometheus--stack-red)

---

## O que é

CloudWatch Sentinel é um agente Claude Code que monitora um cluster Kubernetes em tempo real. Ele dispara sub-agents em paralelo para coletar métricas do Prometheus e analisar o estado dos pods, correlaciona os dados, classifica a severidade e gera runbooks ou relatórios automaticamente — sem intervenção manual.

O projeto demonstra na prática o uso de:
- **Sub-agents paralelos** para investigação simultânea de múltiplas fontes
- **MCP Servers** para integração com Prometheus e kubectl
- **CLAUDE.md** como memória persistente de contexto do ambiente
- **Slash commands** customizados como interface de operação

---

## Arquitetura

```
CLAUDE.md (contexto, thresholds, templates)
        │
        ▼
/sentinel (orquestrador)
        │
   ┌────┴────┐
   ▼         ▼
/collect-  /analyze-       ← paralelo
 metrics    pods
   │         │
   └────┬────┘
        ▼
   /correlate
   (classifica severidade)
        │
   ┌────┴──────────┐
   ▼               ▼
CRITICAL         WARNING / OK
gera runbook     gera relatório
```

### Componentes

| Componente | Função |
|---|---|
| `CLAUDE.md` | Memória do agente: endpoints, thresholds, templates de runbook |
| `/sentinel` | Orquestrador — ponto de entrada, verifica pré-requisitos e consolida |
| `/collect-metrics` | Sub-agent A — consulta Prometheus via PromQL |
| `/analyze-pods` | Sub-agent B — verifica status de pods e deployments via kubectl |
| `/correlate` | Sub-agent C — correlaciona dados e classifica severidade |

---

## Stack

| Camada | Tecnologia |
|---|---|
| Cluster | Minikube (KVM2) — Kubernetes v1.35.1 |
| Monitoramento | kube-prometheus-stack (Prometheus + Grafana + AlertManager) |
| Agente | Claude Code 2.1.76 |
| Integrações | MCP Server Prometheus + MCP Server kubectl |
| Output | Runbooks e relatórios em Markdown |

---

## Pré-requisitos

- [Claude Code](https://claude.ai/code) instalado e autenticado
- Minikube rodando com o namespace `monitoring`
- Helm 3.x
- Node.js (para os MCP Servers via npx)

---

## Setup

### 1. Sobe o stack de monitoramento

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

kubectl create namespace monitoring

helm install prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring \
  --set grafana.adminPassword=admin123
```

Aguarda todos os pods ficarem `Running`:

```bash
kubectl get pods -n monitoring
```

### 2. Clona o repositório

```bash
git clone https://github.com/<seu-usuario>/cloudwatch-sentinel
cd cloudwatch-sentinel
```

### 3. Configura os MCP Servers

```bash
claude mcp add prometheus \
  -e PROMETHEUS_URL=http://localhost:9090 \
  -- npx -y prometheus-mcp-server

claude mcp add kubectl \
  -- npx -y kubectl-mcp-server

claude mcp list
```

Ambos devem aparecer como `Connected`.

### 4. Ativa os port-forwards

```bash
kubectl port-forward svc/prometheus-stack-kube-prom-prometheus -n monitoring 9090:9090 &
kubectl port-forward svc/prometheus-stack-grafana -n monitoring 3000:80 &
```

---

## Uso

Abre o Claude Code no diretório do projeto:

```bash
claude
```

Executa o agente:

```
/sentinel
```

O orquestrador verifica os pré-requisitos, dispara os sub-agents em paralelo, correlaciona os resultados e gera automaticamente o output em `./runbooks/` ou `./reports/`.

---

## Outputs gerados

### Relatório WARNING / OK
```
reports/
└── 2026-03-23_14-45_WARNING.md
```

Contém: métricas coletadas, status dos pods, eventos de Warning categorizados e recomendações priorizadas com comandos prontos.

### Runbook CRITICAL
```
runbooks/
└── 2026-03-23_14-45_CRITICAL_prometheus.md
```

Contém: situação detectada, métricas no momento do incidente, hipóteses de causa raiz, ações recomendadas com checklist e comandos de diagnóstico.

---

## Thresholds

| Métrica | WARNING | CRITICAL |
|---|---|---|
| CPU | > 70% | > 85% |
| Memória | > 75% | > 90% |
| Disco | > 70% | > 85% |
| Pod CrashLoopBackOff | — | imediato |
| Pod Pending > 5min | ✓ | — |

---

## Estrutura do projeto

```
cloudwatch-sentinel/
├── CLAUDE.md                        # Memória e contexto do agente
├── README.md
├── .claude/
│   └── commands/
│       ├── sentinel.md              # Orquestrador
│       ├── collect-metrics.md       # Sub-agent A
│       ├── analyze-pods.md          # Sub-agent B
│       └── correlate.md             # Sub-agent C
├── runbooks/                        # Runbooks CRITICAL gerados
└── reports/                         # Relatórios WARNING/OK gerados
```

---

## Exemplo de output real

O relatório abaixo foi gerado automaticamente pelo agente em execução real contra um cluster Minikube:

```
Severidade: WARNING
CPU: 11.4% | Memória: 45.1% | Disco: 17.65%
Pods Running: 16/16 | Deployments saudáveis: 7/7
64 Warning events identificados como residuais de restart anterior do nó
2 pontos de atenção: storage-provisioner BackOff + readiness probes CoreDNS/Grafana
```

---

## Motivação

Projeto desenvolvido para explorar na prática a arquitetura de agentes Claude Code com sub-agents paralelos e MCP Servers aplicada a um problema real de CloudOps — monitoramento e resposta a incidentes em clusters Kubernetes.

Faz parte de uma trilha de estudos pessoal: **CKA → Claude Code → MLOps**.


