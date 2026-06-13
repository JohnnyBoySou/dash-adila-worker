# worker — agent de provisionamento Hetzner

Agent em Go que roda **na box Hetzner** e provisiona Postgres (e, futuramente,
Redis) como containers Docker, **um por tenant** (container-per-tenant). O control
plane (`back/`) fala com ele via HTTP usando o `HetznerDatabaseProvider`.

## Por que existe

A Hetzner Cloud entrega só uma VM crua — não há Postgres/Redis gerenciado como no
Neon/Upstash. Este agent recebe requisições do control plane e faz
`docker run`/`stop`/`rm` por tenant, devolvendo a connection string. Operações a
nível de host (parar container, destruir volume) não são expressáveis em SQL — daí
o agent.

**Motivação:** custo. Container parado consome ~0 RAM, então `stop`/`start`
(suspend/resume) são a alavanca de custo do modelo — diferente de SaaS, onde
suspend é no-op.

## Design

- **Zero dependências externas** — só a stdlib (`net/http`, `os/exec`). O Docker é
  acionado pelo CLI (`docker run ...`), com args passados como slice (nunca via
  shell → sem injeção).
- **Stateless** — os labels do Docker (`adila.*`) são a única fonte de estado, então
  o agent sobrevive a restart e reflete sempre o que o Docker realmente tem.
- **Porta fixa por tenant** — cada container recebe uma porta de host fixa do range
  `AGENT_PORT_RANGE_START..END` (alocada no create, gravada no label
  `adila.pg.hostport`). Porta efêmera mudaria a cada `stop`/`start` e deixaria a
  `DATABASE_URL` stale após suspend/resume; a porta fixa é estável e remontável mesmo
  com o container parado.
- **Status semântico** — distingue parada intencional (`stopped`) de crash
  (`crashed`) via `State.Status` + `ExitCode` + `Health.Status`. É a vantagem sobre
  APIs que só dizem "rodando ou não".
- **Idempotente** — `POST /v1/resources` deduplica pela `idempotencyKey` (labelada no
  container), cobrindo o retry do job sem duplicar recurso.
- **Testável** — a camada HTTP depende da interface `ContainerRuntime`, com um fake
  em memória para os testes (sem Docker).

## Estrutura

```
worker/
├── main.go                       # entrypoint + graceful shutdown
├── internal/
│   ├── config/config.go          # env → Config (AGENT_TOKEN obrigatório)
│   ├── api/
│   │   ├── types.go              # DTOs (espelham o contrato TS)
│   │   ├── server.go             # handlers + bearer auth + connection URL
│   │   └── server_test.go        # testes com httptest + fake runtime
│   └── runtime/
│       ├── runtime.go            # interface ContainerRuntime + Spec/Instance
│       ├── status.go             # Docker State → status semântico
│       ├── status_test.go        # testes do mapeamento de status
│       ├── docker.go             # impl real (docker CLI)
│       └── fake.go               # impl em memória (testes)
```

## Contrato HTTP

Autenticação: `Authorization: Bearer <AGENT_TOKEN>` em todas as rotas exceto `/healthz`.

| Método | Rota | Descrição |
|--------|------|-----------|
| `GET` | `/healthz` | Liveness (sem auth) |
| `POST` | `/v1/resources` | Provisiona (idempotente por `idempotencyKey`) → 201 |
| `GET` | `/v1/resources/{id}` | Estado do recurso → 200 / 404 |
| `POST` | `/v1/resources/{id}/stop` | Para o container (suspend) → 202 / 404 |
| `POST` | `/v1/resources/{id}/start` | Religa o container (resume) → 202 / 404 |
| `DELETE` | `/v1/resources/{id}?destroyData=true` | Destrói container (+ volume) → 204 |

## Rodar

```bash
cp .env.example .env   # ajuste AGENT_TOKEN
export $(grep -v '^#' .env | xargs)  # ou use um carregador de .env
go run .
```

## Build & testes

```bash
go build ./...
go test ./...
```

Os testes **não** precisam de Docker — usam o fake em memória.

## Segurança

- O agent NUNCA é exposto na internet — fica numa rede privada (WireGuard / rede
  privada Hetzner) + bearer token compartilhado.
- Roda como root-no-Docker = root-na-box → comprometê-lo vaza todos os tenants.
- A senha do Postgres fica num label do container; quem lê labels já tem acesso ao
  Docker (= controle total da box), então isso não piora o modelo de ameaça.
- HMAC assinado por requisição é evolução prevista.

## Fases

- **Fase 2 (esta):** agent contra Docker local.
- **Fase 3:** bootstrap da box Hetzner (Terraform/cloud-init) — primeira box billable.
- **Fase 4:** Redis + idle-stop automático + backups para R2.
