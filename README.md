# HookForge

HookForge is a reliable webhook delivery service written in Go. Producers submit an
event once; HookForge persists it before acknowledging the request and delivers it
to one or more HTTP endpoints with **at-least-once** semantics.

New to webhooks, APIs, or Go? Start with the
[beginner-friendly Russian guide](docs/beginner-guide.ru.md).

The project is intentionally honest about distributed-systems guarantees:
receivers can observe a duplicate when a process dies after the receiver accepts a
request but before HookForge commits success. Every delivery therefore includes a
stable event ID and an HMAC signature so consumers can verify and deduplicate it.

Every API request is authenticated with a high-entropy Bearer key. The key selects
a tenant, and all endpoint, event, delivery, and replay queries are scoped to that
tenant. Endpoint signing secrets are encrypted at rest with AES-256-GCM.

## Current capabilities

- PostgreSQL-backed durable event and delivery model
- transactional schema migrations embedded in the binary
- idempotent event ingestion with transactional endpoint fan-out
- endpoint and delivery query APIs with bounded pagination
- bounded delivery workers with per-endpoint noisy-neighbour isolation
- signed webhooks, full-jitter retries, stale-lease recovery, and a DLQ
- Prometheus metrics, pprof diagnostics, and a provisioned Grafana dashboard
- embedded operator console for endpoints, events, delivery status, and DLQ replay
- Bearer API-key authentication and tenant isolation
- DNS-rebinding-resistant SSRF protection
- AES-256-GCM encryption for endpoint signing secrets
- liveness and database readiness probes
- graceful HTTP shutdown
- container image and local Compose environment

The implementation is organized as separate API, persistence, delivery, and
observability packages so each reliability boundary is testable in isolation.

## Quick start

Requirements: Go 1.26.5+ and PostgreSQL 17+ (or Docker).

```bash
go run ./cmd/hookforge generate-secrets > .env
docker compose up --build
curl http://localhost:8080/readyz
```

On Windows PowerShell, preserve the plain-text environment-file encoding:

```powershell
go run ./cmd/hookforge generate-secrets | Out-File -Encoding ascii .env
docker compose up --build
```

Keep `.env` private and backed up securely. Changing
`HOOKFORGE_SECRET_ENCRYPTION_KEY` makes existing endpoint secrets unreadable.

Open `http://localhost:8080/` and sign in with the value of
`HOOKFORGE_BOOTSTRAP_API_KEY` from `.env`. The console keeps the key only in the
current browser tab and provides endpoint creation, event submission, delivery
filters, status counters, and dead-letter replay. No separate frontend process or
Node.js installation is required.

## API walkthrough

Create an endpoint. Its signing secret is returned once:

```bash
curl -sS http://localhost:8080/v1/endpoints \
  -H "Authorization: Bearer $HOOKFORGE_BOOTSTRAP_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"name":"orders","url":"https://example.com/webhooks"}'
```

Submit an event using the returned endpoint ID:

```bash
curl -sS http://localhost:8080/v1/events \
  -H "Authorization: Bearer $HOOKFORGE_BOOTSTRAP_API_KEY" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: checkout-order-42' \
  -d '{"type":"order.created","payload":{"order_id":"42"},"endpoint_ids":["ENDPOINT_ID"]}'
```

The first request returns `202 Accepted`; a repeat with the same idempotency key
returns the original event with `200 OK` and `"duplicate": true`.

Inspect the DLQ and replay a corrected delivery:

```bash
curl -sS 'http://localhost:8080/v1/deliveries?status=dead' \
  -H "Authorization: Bearer $HOOKFORGE_BOOTSTRAP_API_KEY"
curl -i -X POST http://localhost:8080/v1/deliveries/DELIVERY_ID/replay \
  -H "Authorization: Bearer $HOOKFORGE_BOOTSTRAP_API_KEY"
```

Receivers verify `X-HookForge-Signature`, whose value is
`v1=HMAC_SHA256(secret, timestamp + "." + raw_request_body)`, and deduplicate on
`X-HookForge-Event-ID`.

To provision another isolated tenant and receive its initial API key:

```bash
docker compose run --rm hookforge provision-tenant \
  --slug acme --name "Acme Corporation"
```

Private, loopback, link-local, metadata, and other non-public endpoint addresses
are blocked by default at both registration and connection time. Local webhook
testing can explicitly set `HOOKFORGE_ALLOW_PRIVATE_ENDPOINTS=true`; never enable
that override in production.

Operational dashboards are available at `http://localhost:3000` after Compose
starts. See [docs/runbook.md](docs/runbook.md) for alerting and incident procedures.
The complete HTTP contract is in [docs/openapi.yaml](docs/openapi.yaml).

## Engineering guarantees

| Concern | Design |
| --- | --- |
| Durability | Event and fan-out deliveries are committed atomically |
| Delivery | At least once; no false exactly-once claim |
| Claiming | PostgreSQL row locks with `SKIP LOCKED` |
| Retries | Exponential backoff with full jitter and a bounded attempt count |
| Poison events | Explicit dead-letter state with manual replay |
| Backpressure | Fixed global worker pool and per-endpoint concurrency limits |
| Shutdown | Stop claiming, drain in-flight work, then close dependencies |
| Tenant isolation | Tenant identity comes only from an authenticated API key |
| Secret storage | AES-256-GCM with random nonces and tenant/endpoint-bound AAD |
| SSRF | URL validation plus DNS-aware filtering on every outbound connection |

See [docs/architecture.md](docs/architecture.md) for the system design and failure
model.

## Development

```bash
make test
make test-race
make lint
```

## License

MIT
