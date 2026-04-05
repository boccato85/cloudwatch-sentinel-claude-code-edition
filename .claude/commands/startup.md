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

### 2. Limpar port-forwards órfãos e subir os ausentes

Antes de criar novos port-forwards, verifique se já existem processos obsoletos nas portas alvo e encerre apenas os que estiverem presos (processo existe mas serviço não responde):

```bash
for PORT in 9090 3000 9093; do
  PID=$(lsof -ti tcp:$PORT 2>/dev/null)
  if [ -n "$PID" ]; then
    # porta ocupada mas serviço não respondeu no passo 1 → órfão
    kill $PID 2>/dev/null && echo "Órfão encerrado na porta $PORT (PID $PID)"
  fi
done
```

Execute o bloco acima **apenas para as portas cujos serviços foram identificados como DOWN** no passo 1.

Em seguida, para cada serviço DOWN, inicie o port-forward em background:

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

### 4. Go Agent (Dashboard)

Verifique se o Go agent está respondendo:

```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/api/summary
```

- Se retornar `200`: marque como `OK`.
- Se falhar: inicie o serviço e aguarde:

```bash
cd agent && make start
```

Após o start, aguarde com retries:
- Intervalo: 2 segundos
- Máximo: 10 tentativas
- Critério: `curl http://localhost:8080/api/summary` retornar `200`

Se após 10 tentativas não responder, marque como `FAILED` e exiba o output de `make logs`.

### 5. Relatório de status

Exiba o resultado final no seguinte formato:

```
╔═══════════════════════════════════════════════════════════╗
║   CloudWatch Sentinel - Claude Code Edition — Startup     ║
╚═══════════════════════════════════════════════════════════╝

 Prometheus    (localhost:9090)  →  <STATUS>
 Grafana       (localhost:3000)  →  <STATUS>
 AlertManager  (localhost:9093)  →  <STATUS>
 Go Agent      (localhost:8080)  →  <STATUS>
```

Onde `<STATUS>` é um dos seguintes:
- `✅ OK` — estava acessível antes de qualquer ação
- `✅ STARTED` — estava DOWN, iniciado e serviço confirmado
- `❌ FAILED` — não foi possível estabelecer conexão após tentativas

### 6. Decisão final

- Se **todos** os serviços estiverem `OK` ou `STARTED`: informe que o ambiente está pronto e retorne controle ao usuário.
- Se **algum** estiver `FAILED`: exiba o log de erro relevante e oriente o usuário:
  - Prometheus/Grafana/AlertManager: `kubectl get pods -n monitoring`
  - Go Agent: `cd agent && make logs`

### 🧹 Sanitização de Ambiente
1. Verifique se existem arquivos `.json` residuais na raiz: `ls *.json`
2. Se existirem, execute a limpeza silenciosa: `rm -f *.json`

## Princípios

- Nunca mate port-forwards já existentes — apenas crie os que estão ausentes
- Sempre execute as verificações iniciais em paralelo para ser mais rápido
- Seja objetivo: o output final deve caber em menos de 15 linhas
- Em caso de FAILED, sempre mostrar o log de erro para facilitar diagnóstico
