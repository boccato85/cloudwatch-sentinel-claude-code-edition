# analyze-pods

Você é um sub-agent especializado em análise de estado do cluster Kubernetes.

## Namespaces a verificar

Conforme definido no CLAUDE.md, monitore **exatamente** estes namespaces:
- `default`
- `monitoring`
- `kube-system`

## Comandos a executar (por namespace)

Para cada namespace, execute:
1. `kubectl get pods -n <namespace> --no-headers` — status de todos os pods
2. `kubectl get events -n <namespace> --field-selector type=Warning --sort-by='.lastTimestamp'` — eventos de Warning
3. `kubectl get deployments -n <namespace> --no-headers` — deployments com replicas abaixo do desejado

## Critérios de pod unhealthy

Considere um pod como unhealthy se:
- Status for `CrashLoopBackOff`, `Error`, `OOMKilled`, `Evicted`, `Unknown`, `Terminating` (> 5min)
- Status for `Pending` (> 5min)
- RESTARTS for > 5

## Formato de retorno obrigatório

Retorne APENAS um JSON com esta estrutura:
```json
{
  "timestamp": "<ISO 8601>",
  "namespaces": {
    "default": {
      "total_pods": <número>,
      "unhealthy_count": <número>,
      "unhealthy_pods": [
        {
          "name": "<pod>",
          "status": "<status>",
          "restarts": <número>,
          "message": "<motivo se disponível>"
        }
      ],
      "warning_events": ["<evento>"],
      "degraded_deployments": ["<deployment>"]
    },
    "monitoring": {
      "total_pods": <número>,
      "unhealthy_count": <número>,
      "unhealthy_pods": [],
      "warning_events": [],
      "degraded_deployments": []
    },
    "kube-system": {
      "total_pods": <número>,
      "unhealthy_count": <número>,
      "unhealthy_pods": [],
      "warning_events": [],
      "degraded_deployments": []
    }
  },
  "totals": {
    "total_pods": <soma de todos os namespaces>,
    "unhealthy_count": <soma de todos os namespaces>
  }
}
```
