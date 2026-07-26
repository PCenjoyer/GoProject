# HookForge

HookForge is a reliable webhook delivery service written in Go. Producers submit an
event once; HookForge persists it before acknowledging the request and delivers it
to one or more HTTP endpoints with **at-least-once** semantics.

The project is intentionally honest about distributed-systems guarantees:
receivers can observe a duplicate when a process dies after the receiver accepts a
request but before HookForge commits success. Every delivery therefore includes a
stable event ID and an HMAC signature so consumers can verify and deduplicate it.

## Current capabilities

- PostgreSQL-backed durable event and delivery model
- transactional schema migrations embedded in the binary
- idempotent event ingestion with transactional endpoint fan-out
- endpoint and delivery query APIs with bounded pagination
- bounded delivery workers with per-endpoint noisy-neighbour isolation
- signed webhooks, full-jitter retries, stale-lease recovery, and a DLQ
- Prometheus metrics, pprof diagnostics, and a provisioned Grafana dashboard
- liveness and database readiness probes
- graceful HTTP shutdown
- container image and local Compose environment

The implementation is organized as separate API, persistence, delivery, and
observability packages so each reliability boundary is testable in isolation.

## Quick start

Requirements: Go 1.25+ and PostgreSQL 17+ (or Docker).

```bash
cp .env.example .env
docker compose up --build
curl http://localhost:8080/readyz
```

Without Docker, create the database from `.env.example`, then run:

```bash
go run ./cmd/hookforge
```

## API walkthrough

Create an endpoint. Its signing secret is returned once:

```bash
curl -sS http://localhost:8080/v1/endpoints \
  -H 'Content-Type: application/json' \
  -d '{"name":"orders","url":"https://example.com/webhooks"}'
```

Submit an event using the returned endpoint ID:

```bash
curl -sS http://localhost:8080/v1/events \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: checkout-order-42' \
  -d '{"type":"order.created","payload":{"order_id":"42"},"endpoint_ids":["ENDPOINT_ID"]}'
```

The first request returns `202 Accepted`; a repeat with the same idempotency key
returns the original event with `200 OK` and `"duplicate": true`.

Inspect the DLQ and replay a corrected delivery:

```bash
curl -sS 'http://localhost:8080/v1/deliveries?status=dead'
curl -i -X POST http://localhost:8080/v1/deliveries/DELIVERY_ID/replay
```

Receivers verify `X-HookForge-Signature`, whose value is
`v1=HMAC_SHA256(secret, timestamp + "." + raw_request_body)`, and deduplicate on
`X-HookForge-Event-ID`.

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
