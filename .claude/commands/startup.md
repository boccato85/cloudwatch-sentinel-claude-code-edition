# startup

Você é o agente de inicialização do CloudWatch Sentinel - Claude Code Edition. Sua tarefa é garantir que todos os serviços de monitoramento estejam acessíveis antes de qualquer operação.

## Serviços a verificar

| Serviço | URL local | Serviço K8s | Porta |
|---|---|---|---|
| Prometheus | http://localhost:9090 | svc/prometheus-stack-kube-prom-prometheus | 9090:9090 |
| Grafana | http://localhost:3000 | svc/prometheus-stack-grafana | 3000:80 |
| AlertManager | http://localhost:9093 | svc/prometheus-stack-kube-prom-alertmanager | 9093:9093 |

Namespace: `monitoring`

## Fluxo de execução

### 0. Verificação do Minikube

Antes de qualquer ação, verifique se o Minikube está rodando:

```bash
minikube status
```

- Se `host: Running` e `apiserver: Running`: prossiga para o passo 1.
- Se qualquer componente estiver `Stopped` ou ausente: execute o start e aguarde:

```bash
minikube start
```

Após o start, aguarde o cluster ficar pronto com retries:
- Intervalo: 15 segundos
- Máximo: 20 tentativas (~5 minutos)
- Critério de pronto: `kubectl get nodes` retornar `Ready`

```bash
for i in $(seq 1 20); do
  STATUS=$(kubectl get nodes --no-headers 2>/dev/null | awk '{print $2}' | head -1)
  echo "Tentativa $i — Node status: $STATUS"
  [[ "$STATUS" == "Ready" ]] && echo "CLUSTER_READY" && break
  sleep 15
done
```

Se após 20 tentativas o cluster não estiver `Ready`, exiba erro e **encerre com FAILED** — não prossiga para os passos seguintes.

Exiba ao final desta etapa:
- `[MINIKUBE] já estava Running — prosseguindo` ou
- `[MINIKUBE] iniciado com sucesso — cluster Ready` ou
- `[MINIKUBE] FAILED — cluster não respondeu após 5 minutos`

### 1. Verificação inicial (em paralelo)

Para cada serviço, tente acessar o endpoint de health:
- Prometheus: `curl -s -o /dev/null -w "%{http_code}" http://localhost:9090/-/healthy`
- Grafana: `curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/api/health`
- AlertManager: `curl -s -o /dev/null -w "%{http_code}" http://localhost:9093/-/healthy`

Considere o serviço **UP** se o HTTP status code for `200`. Qualquer outro resultado (erro de conexão, timeout, código diferente) indica **DOWN**.

### 2. Subir port-forwards para serviços DOWN

Para cada serviço identificado como DOWN, execute o port-forward em background:

```bash
kubectl port-forward svc/prometheus-stack-kube-prom-prometheus -n monitoring 9090:9090 > /tmp/pf-prometheus.log 2>&1 &
kubectl port-forward svc/prometheus-stack-grafana -n monitoring 3000:80 > /tmp/pf-grafana.log 2>&1 &
kubectl port-forward svc/prometheus-stack-kube-prom-alertmanager -n monitoring 9093:9093 > /tmp/pf-alertmanager.log 2>&1 &
```

Execute apenas os comandos dos serviços que estiverem DOWN.

### 3. Aguardar confirmação

Após iniciar os port-forwards, aguarde os serviços ficarem responsivos com retries:
- Intervalo entre tentativas: 2 segundos
- Máximo de tentativas: 10 (total de ~20 segundos)
- Use os mesmos endpoints de health da etapa 1

Se após 10 tentativas o serviço ainda não responder, marque como **FAILED**.

### 4. Relatório de status

Exiba o resultado final no seguinte formato:

```
╔═══════════════════════════════════════════════════════════╗
║   CloudWatch Sentinel - Claude Code Edition — Startup     ║
╚═══════════════════════════════════════════════════════════╝

 Prometheus    (localhost:9090)  →  <STATUS>
 Grafana       (localhost:3000)  →  <STATUS>
 AlertManager  (localhost:9093)  →  <STATUS>
```

Onde `<STATUS>` é um dos seguintes:
- `✅ OK` — estava acessível antes de qualquer ação
- `✅ STARTED` — estava DOWN, port-forward iniciado e serviço confirmado
- `❌ FAILED` — não foi possível estabelecer conexão após tentativas

### 5. Decisão final

- Se **todos** os serviços estiverem `OK` ou `STARTED`: informe que o ambiente está pronto e retorne controle ao usuário.
- Se **algum** estiver `FAILED`: exiba o log de erro (`/tmp/pf-<serviço>.log`) e oriente o usuário a verificar os pods do namespace `monitoring` com `kubectl get pods -n monitoring`.

### 🧹 Sanitização de Ambiente
1. Verifique se existem arquivos `.json` residuais na raiz: `ls *.json`
2. Se existirem, execute a limpeza silenciosa: `rm -f *.json`
3. (Opcional) Limpar eventos antigos do K8s para evitar falsos positivos de reboots passados: `kubectl delete events --all -A`

## Princípios

- Nunca mate port-forwards já existentes — apenas crie os que estão ausentes
- Sempre execute as verificações iniciais em paralelo para ser mais rápido
- Seja objetivo: o output final deve caber em menos de 15 linhas
- Em caso de FAILED, sempre mostrar o log de erro para facilitar diagnóstico
