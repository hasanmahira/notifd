# notifd — Event-Driven Notification System

Go service that accepts SMS / Email / Push notification requests, queues them asynchronously, delivers them through a simulated external provider under a strict **100 msg/s/channel** rate limit, and streams real-time status over WebSocket.

## What's inside

- **Docker Compose** — one command brings up the full stack: `docker-compose.yml` (+ optional `docker-compose.observability.yml`). See [Quickstart](#quickstart).
- **API docs** — full OpenAPI 3.0 spec (`docs/openapi.yaml`) + Swagger UI at `/swagger/`.
- **Database migrations** — versioned schema via `golang-migrate/migrate` (`migrations/001_init.{up,down}.sql`, `002_templates.{up,down}.sql`), applied with `make migrate-up`.
- **Test suite** — single command: `make test` → `go test -race ./...`.

## Feature coverage

| Category | Feature | Status |
|---|---|---|
| **Core** | Notification Management API (create single, batch ≤1000, query by id/batch id, cancel, list w/ filters + keyset pagination) | ✅ |
| **Core** | Async processing engine (Asynq + Redis) | ✅ |
| **Core** | Global rate limit 100 msg/s/channel (distributed Lua token bucket) | ✅ |
| **Core** | Priority queues (high / normal / low, weighted 6/3/1) | ✅ |
| **Core** | Content validation (per-channel size + required fields + batch size) | ✅ |
| **Core** | External provider integration (webhook.site-shape mock with failure injection) | ✅ |
| **Resilience & scheduling** | Idempotency (SHA-256 body hash, same-key-different-body → 409) | ✅ |
| **Resilience & scheduling** | Delivery + retry logic (exp backoff + jitter, DLQ on exhaustion) | ✅ |
| **Resilience & scheduling** | Failure handling (circuit breaker, Retry-After honoring, transient vs terminal classification) | ✅ |
| **Resilience & scheduling** | Scheduled notifications (`scheduled_at` + `asynq.ProcessAt`) | ✅ |
| **Resilience & scheduling** | **Template system** (DB-stored, CRUD + preview, Go text/template with helper funcs — see §Templates) | ✅ |
| **Operations** | Observability (`/metrics`, structured slog + correlation IDs, `/healthz` + `/readyz`) | ✅ |
| **Operations** | WebSocket real-time updates (Redis Pub/Sub bridge) | ✅ |
| **Operations** | Distributed tracing (OpenTelemetry SDK, stdouttrace exporter) | ✅ |
| **Operations** | GitHub Actions CI/CD (lint + race-tests + service containers) | ✅ |
| **Operations** | Graceful shutdown (SIGTERM drain, WS `1001 Going Away`) | ✅ |
| **Operations** | Multi-replica API behind Traefik + multi-replica workers (`make scale`) | ✅ |
| **Operations** | Grafana + Loki + Promtail + Prometheus observability overlay (`make obs-up`) | ✅ |

---

## Quickstart

```bash
docker compose up -d --build
# wait ~15s for services to go healthy
curl -s http://localhost:8080/healthz
curl -s http://localhost:8080/readyz

# fire a seed demo that hits every channel + a batch and lists the result
./scripts/seed.sh

# Swagger UI
open http://localhost:8080/swagger/
```

Everything runs in one command: `api`, `worker`, `postgres:16`, `redis:7`, `mockserver` (plays the role of webhook.site locally), plus a one-shot migrator.

---

## Architecture

```
                                         ┌──────────────────────────┐
  HTTP client  ───┐                       │   notification_messages  │
   (curl, UI)    ▼                        │   user_notifications     │
           ┌────────┐  JWT + Idempotency  │   fcm_tokens             │
           │ chi API│◀───────────────────▶│   (PostgreSQL 16)        │
           └────────┘                     └──────────────────────────┘
               │                                      ▲
               │ enqueue (Asynq/Redis)                │
               ▼                                      │
           ┌────────┐  Redis token bucket (Lua)  ┌────────────┐
           │ Worker │◀──────────────────────────▶│  Redis 7   │
           └────────┘   circuit breaker          │  pub/sub   │
               │        │                        └────────────┘
               │        ▼                              │ publishes status events
               │   ┌──────────┐                        ▼
               │   │ webhook  │               ┌─────────────────┐
               └──▶│  client  │──▶ 202 OK     │   WS hub on API │──▶ ws client
                   └──────────┘               │ (Redis Pub/Sub) │
                                              └─────────────────┘
```

### Layers (Clean Architecture, 4-layer)

```
cmd/
  api/           HTTP entrypoint
  worker/        Asynq worker entrypoint
internal/
  domain/        pure business types, ZERO third-party deps
  usecase/       orchestration + Repository / Publisher / Provider ports
  infrastructure/
    store/       pgx repositories
    queue/       Asynq client + server + depth sampler
    provider/    webhook client + sony/gobreaker circuit breaker
    ratelimit/   Redis token bucket (atomic Lua)
    pubsub/      Redis publisher for status events
    idempotency/ Redis-backed Idempotency-Key store
    auth/        JWT HS256 verifier
    observ/      slog + prometheus + OTel stdout
  delivery/
    http/        chi router, middleware, handlers
    websocket/   hub + per-connection client + upgrade handler
```

Dependencies flow inward: `delivery` and `infrastructure` depend on `usecase` and `domain`; `domain` depends on nothing.

---

## API surface

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/v1/notifications`             | Create one notification (Idempotency-Key required, content OR template_id+variables) |
| POST   | `/v1/notifications/batch`       | Create ≤1000 notifications |
| GET    | `/v1/notifications/:id`         | Fetch by id |
| POST   | `/v1/notifications/:id/cancel`  | Cancel pending / scheduled |
| GET    | `/v1/notifications?status=&channel=&from=&to=&cursor=&limit=` | Keyset pagination |
| GET    | `/v1/batches/:batch_id`         | Batch aggregate summary |
| POST   | `/v1/templates`                 | Create template (Idempotency-Key required) |
| GET    | `/v1/templates`                 | List templates |
| GET    | `/v1/templates/:id`             | Fetch template by id |
| PUT    | `/v1/templates/:id`             | Update subject + body (name/channel/locale immutable) |
| DELETE | `/v1/templates/:id`             | Delete a template |
| POST   | `/v1/templates/:id/preview`     | Dry-run render (no persistence) |
| GET    | `/v1/ws/batch/:batch_id`        | Real-time status WebSocket |
| GET    | `/healthz`                      | Liveness |
| GET    | `/readyz`                       | Readiness (PG + Redis ping) |
| GET    | `/metrics`                      | Prometheus |
| GET    | `/swagger/`                     | OpenAPI UI |
| GET    | `/openapi.yaml`                 | OpenAPI 3.0 spec |

Authentication: `Authorization: Bearer <jwt>` (HS256). Demo JWTs are produced by `./scripts/gen_jwt.sh`.

### Quick curl walk-through

```bash
TOKEN="$(./scripts/gen_jwt.sh 3600)"

# 1. create
curl -sS -X POST http://localhost:8080/v1/notifications \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{"recipient":"+905551234567","channel":"sms","priority":"high",
       "content":{"text":"hello from notifd"}}' | jq

# 2. idempotent replay (same key, same body) returns the same id
# run the same command again; X-Idempotent-Replayed: true comes back

# 3. fetch status
curl -sS -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/v1/notifications/<id> | jq

# 4. cancel while pending
curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  http://localhost:8080/v1/notifications/<id>/cancel | jq

# 5. list
curl -sS -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/v1/notifications?status=sent&limit=10" | jq

# 6. 500-row burst (rate-limited at 100/s/channel by the worker)
./scripts/load_test.sh 500 sms

# 7. live stream for a batch
wscat -c "ws://localhost:8080/v1/ws/batch/<batch_id>?token=$TOKEN"
```

---

## Configuration

All via environment variables. Defaults target local `docker compose`.

| Var | Default | Purpose |
|-----|---------|---------|
| `HTTP_ADDR` | `:8080` | API listen address |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `POSTGRES_DSN` | `postgres://notifd:notifd@postgres:5432/notifd?sslmode=disable` | |
| `POSTGRES_MAX_CONNS` | `25` | pgx pool |
| `REDIS_ADDR` | `redis:6379` | |
| `WEBHOOK_URL` | `http://mockserver:1080/webhook` | External provider (webhook.site in production) |
| `WEBHOOK_TIMEOUT` | `5s` | HTTP client timeout |
| `JWT_SECRET` | `dev-secret-do-not-use-in-prod` | HS256 signing key — **required in production** |
| `JWT_ISSUER` | `notifd` | |
| `JWT_TTL` | `24h` | Default TTL when issuing tokens |
| `WORKER_CONCURRENCY` | `10` | Asynq concurrent task handlers |
| `WORKER_MAX_RETRY` | `8` | Retries before a task moves to `dead_letter` |
| `WORKER_SHUTDOWN_TIMEOUT` | `30s` | Graceful drain window |
| `OTEL_ENABLED` | `true` | Enable OTel stdout exporter |
| `MOCK_FORCE_STATUS` (mock only) | `0` | Force the mock webhook to respond with this status on every POST. `0` = normal 202 behavior. |

---

## Templates

Notifications can be sent with **raw `content`** or by referencing a **stored template** + a `variables` map. The API renders the template before enqueue so the worker stays template-unaware and idempotency hashing sees the final bytes that will hit the provider.

### Quick walk-through

```bash
TOKEN="$(./scripts/gen_jwt.sh 3600)"

# create a template
curl -sS -X POST http://localhost:8080/v1/templates \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "order-shipped",
    "channel": "email",
    "locale": "en-US",
    "subject_tmpl": "Order {{.orderID}} is on the way",
    "body_tmpl": "Hi {{title .name}}, your order of {{truncate 40 .items}} ships today."
  }' | jq
# → {"id":"<uuid>", ...}

# preview without sending
curl -sS -X POST "http://localhost:8080/v1/templates/<id>/preview" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"variables":{"orderID":"A-42","name":"mahir","items":"socks, books"}}' | jq
# → {"subject":"Order A-42 is on the way","body":"Hi Mahir, your order of socks, books ships today."}

# send a notification using the template
curl -sS -X POST http://localhost:8080/v1/notifications \
  -H "Authorization: Bearer $TOKEN" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "Content-Type: application/json" \
  -d '{
    "recipient": "demo@example.com",
    "channel": "email",
    "priority": "high",
    "template_id": "<id>",
    "variables": {"orderID":"A-42","name":"mahir","items":"socks, books"}
  }' | jq

# list / update / delete
curl -sS "http://localhost:8080/v1/templates"                                        -H "Authorization: Bearer $TOKEN" | jq
curl -sS -X PUT "http://localhost:8080/v1/templates/<id>" -d '{"subject_tmpl":"...", "body_tmpl":"..."}' -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" | jq
curl -sS -X DELETE "http://localhost:8080/v1/templates/<id>"                         -H "Authorization: Bearer $TOKEN" -w "%{http_code}\n"
```

### Template engine details

- **Syntax:** Go `text/template` — `{{.var}}`, `{{if .x}}…{{end}}`, `{{range .items}}…{{end}}`.
- **Missing-variable behavior:** `missingkey=error` — typos return `400 invalid_input` instead of rendering `<no value>`.
- **Helper functions** available in every template:

| Helper | Example | Result |
|---|---|---|
| `upper`  | `{{upper .name}}` with `{"name":"mahir"}` | `MAHIR` |
| `lower`  | `{{lower .shout}}` with `{"shout":"HI"}` | `hi` |
| `title`  | `{{title .s}}` with `{"s":"hello world"}` | `Hello World` |
| `truncate` | `{{truncate 5 .s}}` with `{"s":"abcdefghij"}` | `abcde…` |
| `default` | `{{default "guest" .name}}` with `{"name":""}` | `guest` |
| `formatDate` | `{{formatDate .ts "2006-01-02"}}` with `{"ts":"2026-04-23T12:00:00Z"}` | `2026-04-23` |

- **Per-channel rules:** email templates **require** `subject_tmpl`; SMS and push **reject** `subject_tmpl`.
- **Rendering location:** API-side before enqueue. Two consequences:
  - Idempotency hashing sees the rendered body — replays with the same `(template_id, variables)` stay deterministic.
  - Templates can be edited / deleted without affecting already-enqueued notifications; past notifications keep a `template_id` pointer purely for audit.
- **Templates referencing another template:** not supported (no recursive render). Keep composition in application code.

---

## Scalability

The stack ships with **Traefik** as a reverse proxy in front of the API so `docker compose up --scale api=N --scale worker=M` works out of the box — no manual load balancer to configure.

```
┌─────────────┐     :8080  ┌──────────┐     :8080  ┌──────────────┐
│ curl / UI   │──────────▶│  Traefik │─────▶ rr ─▶ │  api-1, api-2│
└─────────────┘           └──────────┘            └──────┬───────┘
                                                         │ enqueue
                                                         ▼
                                   ┌────────────────────────────────┐
                                   │  worker-1, worker-2, worker-3  │
                                   │  share one Redis token bucket  │
                                   └────────────────────────────────┘
```

### One command to scale

```bash
make scale      # same as: docker compose up -d --build --scale api=2 --scale worker=3

open http://localhost:8080              # app traffic — round-robined across api replicas
open http://localhost:8081/dashboard/   # Traefik routing view — live list of upstreams
```

Default non-scaled `make up` runs `api=1`, `worker=1` and is what every other piece of the README assumes.

### Proven behavior under load

| Replicas | 1000-item SMS batch | Observed rate | Over-rate violations |
|---|---|---|---|
| api=1, worker=1 | drains in ~11 s | ≈ 90 msg/s | 0 |
| api=2, worker=3 | drains in ~30 s | ≈ 35 msg/s (retry thrash) | 0 |

The **100 msg/s/channel cap is never exceeded** regardless of replica count — the Redis Lua token bucket is atomic, so all workers share the same counter. The observed throughput gap at higher replica counts is a known characteristic of the poll-retry pattern: when the bucket is dry, every dequeued task makes a Redis round trip, fails, and re-enqueues. A **lease-based limiter** (each worker reserves a shard of the budget for a fixed window) is the production fix — deliberately deferred as future work since correctness, not throughput, was this project's primary design goal.

### How the API stays correct under scale

API replicas are stateless per request; every piece of cross-request state lives outside the process:
- **Postgres** — notifications, templates, the durable idempotency backstop via `user_notifications.idempotency_key` unique index.
- **Redis** — idempotency claim cache, rate-limit bucket, Asynq queue, status Pub/Sub.

**WebSocket sticky-session cookie** (`notifd_sticky`) is set by Traefik so a browser's upgrade lands on the same replica throughout the life of the connection. The Pub/Sub bridge tolerates cross-replica delivery anyway — sticky is just a latency optimization.

### What intentionally stays single-instance

- **Postgres** — single primary. A read-replica story is out of scope / future work. `POSTGRES_MAX_CONNS=25` per app instance is sized so two APIs × 25 + three workers × 25 = 125 < typical Postgres `max_connections=200` default.
- **Redis** — single instance. In production we'd either split Redis by concern (idempotency + cache on one, Asynq on another, Pub/Sub on a third) or move to Redis Cluster. Not required at this scale.
- **Traefik** — single edge. Real clusters would run multiple edges behind a floating IP.
- **Mockwebhook** — by definition a stand-in for `webhook.site`; no reason to replicate.

### Graceful shutdown under scale

Every component drains on SIGTERM. API: `http.Server.Shutdown(30s)` + WebSocket hub closes connections with code `1001 Going Away` (clients reconnect to any other pod). Worker: `asynq.Server.Shutdown()` finishes in-flight tasks within `WORKER_SHUTDOWN_TIMEOUT`. In-flight tasks that don't finish return to the queue and are picked up by the next worker — Asynq's at-least-once delivery guarantee. The idempotency mechanism (API key + DB unique index) prevents duplicate provider calls on reprocessing.

### Failure-injection for DLQ exercise

The bundled `mockwebhook` can be flipped to force failures without a restart:

```bash
curl http://localhost:1080/admin/force?status=500   # force 500 on every POST
curl http://localhost:1080/admin/reset              # back to 202 happy path
curl http://localhost:1080/admin/stats              # {total, forced, ok, force}
```

Combined with `WORKER_MAX_RETRY=2`, you can observe the full retry → DLQ flow in under 5 seconds:
```bash
WORKER_MAX_RETRY=2 docker compose up -d api
curl http://localhost:1080/admin/force?status=500
# POST a notification → watch notifd_notifications_retry_total and
# notifd_notifications_dead_letter_total increment on the worker /metrics
```

---

## Observability

- **Structured logs**: `slog` JSON handler with a `ContextHandler` that pulls `request_id` and `notification_id` out of the context on every log line.
- **Correlation ID**: `X-Request-ID` is read / generated at the API edge, injected into the Asynq task payload, and restored into the worker's logger context — one id tracks a request from HTTP in to provider out.
- **PII masking**: `domain.Recipient` implements `slog.LogValuer` so phone numbers render as `+90***4567` and emails as `u***@example.com` in logs.
- **Metrics** at `/metrics`:
  - `notifd_http_request_duration_seconds` histogram (route, method, status)
  - `notifd_notifications_created_total{channel,priority}`
  - `notifd_notifications_sent_total{channel,status}`
  - `notifd_notifications_retry_total{channel}`
  - `notifd_notifications_dead_letter_total{channel}`
  - `notifd_ratelimit_deny_total{channel}`
  - `notifd_queue_depth{queue,state}` (sampled every 5s from Asynq inspector)
  - `notifd_circuit_breaker_state{provider}`
  - `notifd_websocket_active_connections`, `notifd_websocket_messages_sent_total`
- **Tracing**: OpenTelemetry SDK with `stdouttrace` exporter. Spans appear in the same log stream as the app — no Jaeger container in docker-compose.
- **Liveness** (`/healthz`) and **readiness** (`/readyz`, checks PG + Redis with a 500ms budget).

### How do I actually see the logs?

Two options — quick tail vs. full dashboard.

**Quick tail** (always available, no extra containers):
```bash
docker compose logs -f api worker                                         # tail everything live
docker compose logs api | jq 'select(.request_id=="abc-123")'             # one correlation trail
docker compose logs worker | jq 'select(.level=="ERROR")'                 # errors only
docker compose logs --since=5m worker | jq -r '[.ts, .msg] | @tsv'        # compact, time-sorted
```

**Full dashboard** — Grafana + Loki + Promtail + Prometheus shipped as a single-file overlay (`docker-compose.observability.yml`, ~60 lines) with everything pre-provisioned. Boots in ~15 s.

```bash
make obs-up
#  Grafana      http://localhost:3000         (admin / admin)
#  Prometheus   http://localhost:9090
#  Loki         http://localhost:3100/ready
#  App          http://localhost:8080
#  Traefik      http://localhost:8081/dashboard/
```

The **Notifd — Overview** dashboard appears automatically with panels for:
- Notifications sent/sec per channel
- HTTP p95 latency by route
- Asynq queue depth (pending / active / scheduled / retry / archived per queue)
- Rate-limit denials per channel
- Circuit breaker state (colour-coded closed/half-open/open)
- Dead-letter total, WebSocket active connections, provider p95
- Live logs panels (errors only + all) queryable by `service` / `level` / `request_id`

Log-to-metric correlation works via the `request_id` derived field — click a log line in Loki, jump to Prometheus time range around that request. Promtail tails the Docker socket directly so no app changes are needed; our existing `slog` JSON output is Loki-ready as-is.

Tear down the observability overlay while keeping the app stack running: `make obs-down`.

### Idempotency behavior in depth

The middleware enforces **strict body equality** for key reuse:

| Scenario | Result |
|---|---|
| Same `Idempotency-Key` + same body | `201 Created` replay with `X-Idempotent-Replayed: true`, no duplicate provider call |
| Same `Idempotency-Key` + **different** body | `409 Conflict` with `error: "idempotency_key_reused_different_body"` |
| Same key while first is still in flight (< 5s) | Poll the response cache for up to 5 s, then return stored response or `409` with `Retry-After: 2` |
| Key expired (24 h TTL) | Next request treated as fresh |
| Redis unavailable | Fall-through to handler; the DB unique index on `user_notifications.idempotency_key` is the durable backstop |

The hash is SHA-256 of the raw request body, stored as a parallel Redis key (`idem:<key>:hash`). This closes the Stripe-style same-key-different-payload attack without adding a second RTT (the body is buffered once at the middleware entry).

---

## Design tradeoffs

- **Fan-out schema.** `notification_messages` holds content once; `user_notifications` holds per-recipient delivery rows linked by FK. Trade: a join on every status read. Payoff: identical-content batches don't pay write amplification.
- **UUIDv7 primary keys.** Time-ordered, coordination-free, near-identical B-tree locality to BigInt sequences at our scale. `google/uuid.NewV7()`.
- **Idempotency at two layers, guaranteed by one.** Redis `SET NX EX 24h` for fast dedup; `user_notifications.idempotency_key` unique index is the durable backstop if Redis falls over mid-request.
- **Rate-limit rejection ≠ failure.** A typed `*RateLimitError` bubbles out of the worker; Asynq's `IsFailure` returns false and `RetryDelayFunc` re-enqueues after the limiter's suggested delay. Retries don't burn against real provider flakiness.
- **Circuit breaker trips on 5 consecutive failures or >50% failure rate in a 60s window.** 60s open → half-open probe → recovery.
- **Asynq priority queues weighted 6/3/1 (high/default/low).** Matches the expected flash-sale arrival mix. Low never strictly starves (weighted round-robin). Numbers are tunable in code.
- **Graceful shutdown is global.** Both API and worker respect SIGTERM with a 30s drain window; WebSocket clients see close code `1001 Going Away` and can reconnect to any other API pod.
- **One Redis.** Idempotency, rate-limit state, Asynq queues, and Pub/Sub all ride one instance. Accepted for demo; in production split Redis by concern or move to a dedicated stream/broker for idempotency and rate limiting.

---

## Future work (not shipped)

- **Lease-based rate limiter.** Replace the per-task Redis token-bucket poll with a lease model — each worker reserves a fraction of the per-second budget for, say, 100 ms and satisfies local tasks from that reservation. Drops Redis round trips by ~10×, closes the observed throughput gap at high replica counts.
- **Provider callback ingestion** (webhook → us) to distinguish `provider_accepted` from `delivered`.
- **Monthly partitioning** of `user_notifications` once row count justifies it.
- **Dead-letter inspection / replay API** (`GET /v1/admin/dead_letter`, `POST /v1/admin/dead_letter/:id/replay`).
- **JWKS** (asymmetric JWT) + key rotation.
- **Redis by concern** — separate instances for idempotency, rate limit state, Asynq queues, Pub/Sub — or Redis Cluster.
- **Read replicas** for Postgres behind a router so list/get are served off replicas while writes continue to the primary.

---

## Development

```bash
make up           # bring up docker stack (api, worker, traefik, postgres, redis, mock, migrate)
make scale        # same + api=2, worker=3 behind Traefik
make obs-up       # stack + Grafana/Loki/Promtail/Prometheus dashboards at :3000
make obs-down     # tear down just the observability overlay
make build        # compile api + worker + mockwebhook locally
make test         # go test -race -count=1 ./... (portable, no coverage)
make test-cover   # adds -coverprofile + summary; needs full Go toolchain (covdata)
make lint         # golangci-lint (auto-installs if missing)
make migrate-up   # apply migrations against running postgres
make logs         # tail api + worker logs
make demo         # seed + list
make load         # 500-msg smoke against sms channel
make down         # teardown
```

CI runs `golangci-lint` and `go test -race` against Postgres + Redis services on every push (see `.github/workflows/ci.yml`).

---

## License

MIT — see [LICENSE](LICENSE).
