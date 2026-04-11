# Baseline — full review pass

- Data/hora (UTC): 2026-04-11T19:35:02Z
- Branch: `hardening/full-review-pass`
- Commit base: `e85315e`

## Build/Test baseline

- `cd agent && go build ./...` → ✅ sucesso
- `cd agent && go test ./...` → ✅ sucesso (`[no test files]`)

## Smoke inicial dos endpoints

Execução usada para smoke:

- `DB_USER=postgres DB_PASSWORD=postgres DB_NAME=sentinel_db DB_HOST=localhost DB_SSLMODE=disable ./sentinel-agent`

Resultado:

- `GET /api/summary` → ❌ HTTP `000`
- `GET /api/metrics` → ❌ HTTP `000`
- `GET /api/history` → ❌ HTTP `000`

Diagnóstico observado em log:

- `database ping failed err="dial tcp [::1]:5432: connect: connection refused"`

Status inicial consolidado:

- Build/Test do agente está íntegro.
- Smoke de endpoints bloqueado por indisponibilidade de PostgreSQL local no ambiente de execução.
