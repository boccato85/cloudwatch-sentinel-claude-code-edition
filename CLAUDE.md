# CloudWatch Sentinel — Agente de Monitoramento Inteligente

## Visão Geral
Agente Claude Code que monitora um cluster Kubernetes local via Prometheus e Grafana,
detecta anomalias, investiga causas raiz com sub-agents paralelos e gera runbooks
automáticos de resposta a incidentes.

## Ambiente

### Cluster
- Plataforma: Minikube (KVM2) — Kubernetes v1.35.1
- Host: Fedora 43 — i7-14900HX, 32GB DDR5, RTX 4070
- Namespace de monitoramento: `monitoring`

### Namespaces Monitorados

| Namespace | Descrição |
|---|---|
| `default` | Workloads de aplicação do usuário |
| `monitoring` | Stack de observabilidade (Prometheus, Grafana, AlertManager) |
| `kube-system` | Componentes internos do Kubernetes |

Todos os sub-agents que interagem com kubectl devem verificar **estes três namespaces**.
Novos namespaces devem ser adicionados nesta tabela para serem incluídos no monitoramento.

### Serviços (acessar via port-forward)
| Serviço | ClusterIP | Porta | Port-forward local |
|---|---|---|---|
| Prometheus | 10.97.116.165 | 9090 | localhost:9090 |
| Grafana | 10.98.186.213 | 80 | localhost:3000 |
| AlertManager | 10.102.23.51 | 9093 | localhost:9093 |
| Node Exporter | 10.99.175.64 | 9100 | localhost:9100 |

### Comandos para ativar port-forwards
````bash
kubectl port-forward svc/prometheus-stack-kube-prom-prometheus -n monitoring 9090:9090 &
kubectl port-forward svc/prometheus-stack-grafana -n monitoring 3000:80 &
kubectl port-forward svc/prometheus-stack-kube-prom-alertmanager -n monitoring 9093:9093 &
````

## Thresholds de Alerta

### CPU
| Nível | Threshold | Ação |
|---|---|---|
| WARNING | > 70% por 5min | Gera relatório |
| CRITICAL | > 85% por 2min | Gera runbook + notifica |

### Memória
| Nível | Threshold | Ação |
|---|---|---|
| WARNING | > 75% | Gera relatório |
| CRITICAL | > 90% | Gera runbook + notifica |

### Disco
| Nível | Threshold | Ação |
|---|---|---|
| WARNING | > 70% usado | Gera relatório |
| CRITICAL | > 85% usado | Gera runbook + notifica |

### Pods Kubernetes
| Condição | Nível | Ação |
|---|---|---|
| Pod em CrashLoopBackOff | CRITICAL | Runbook imediato |
| Pod em Pending > 5min | WARNING | Investigar recursos |
| Deployment com replicas < desired | WARNING | Verificar eventos |

## Arquitetura dos Sub-Agents

### Orquestrador (`/sentinel`)
Ponto de entrada principal. Inicializa os sub-agents em paralelo e consolida
os resultados para classificação de severidade.

### Sub-Agent A — Coletor de Métricas (`/collect-metrics`)
Responsabilidade: Consultar Prometheus API e retornar métricas atuais.
- Endpoint: `http://localhost:9090/api/v1/query`
- Retorno esperado: JSON com métricas de CPU, memória, disco e rede

### Sub-Agent B — Analisador de Pods (`/analyze-pods`)
Responsabilidade: Verificar status de todos os pods no cluster.
- Ferramenta: kubectl
- Retorno esperado: lista de pods com status anômalo

### Sub-Agent C — Correlacionador (`/correlate`)
Responsabilidade: Receber outputs dos Sub-Agents A e B, correlacionar,
classificar severidade e decidir o próximo passo.
- Input: resultados dos dois sub-agents anteriores
- Output: severidade (CRITICAL | WARNING | OK) + contexto

## Formato dos Runbooks Gerados

Todo runbook deve ser salvo em `./runbooks/` com nomenclatura:
`YYYY-MM-DD_HH-MM_<severidade>_<componente>.md`

### Estrutura obrigatória do runbook:
````markdown
# Runbook — <título do incidente>
**Data/Hora:** <timestamp>
**Severidade:** CRITICAL | WARNING
**Componente afetado:** <nome>

## Situação Detectada
<descrição objetiva do que foi encontrado>

## Métricas no Momento do Incidente
<valores coletados>

## Hipóteses de Causa Raiz
1. <hipótese 1>
2. <hipótese 2>

## Ações Recomendadas
- [ ] <ação 1>
- [ ] <ação 2>

## Comandos de Diagnóstico
```bash
<comandos relevantes>
```

## Escalonamento
Se não resolvido em 15min: revisar logs com `kubectl logs -n <namespace> <pod>`
````

## Comportamento Esperado do Agente

- **Sempre** iniciar verificando se os port-forwards estão ativos
- **Sempre** rodar Sub-Agent A e B em paralelo (não em série)
- **Nunca** tomar ações destrutivas no cluster sem confirmação explícita
- **Sempre** salvar runbooks gerados no diretório `./runbooks/`
- Em caso de status OK, gerar apenas um resumo curto em `./reports/`

## Estrutura de Diretórios
````
cloudwatch-sentinel/
├── CLAUDE.md                  # Este arquivo
├── .claude/
│   └── commands/
│       ├── sentinel.md        # Comando principal
│       ├── collect-metrics.md # Sub-agent A
│       ├── analyze-pods.md    # Sub-agent B
│       └── correlate.md       # Sub-agent C
├── runbooks/                  # Runbooks gerados automaticamente
├── reports/                   # Relatórios de status OK
└── README.md                  # Documentação do projeto
````

