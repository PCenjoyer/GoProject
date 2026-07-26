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
- liveness and database readiness probes
- graceful HTTP shutdown
- container image and local Compose environment

The event API, worker pool, retries, DLQ, metrics, and replay tooling are developed
as separate, reviewable stages.

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

