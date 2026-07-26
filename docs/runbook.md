# Operations runbook

## Health and diagnostics

- `GET :8080/healthz` verifies the process is alive.
- `GET :8080/readyz` verifies PostgreSQL is reachable.
- `GET :9090/metrics` exposes Prometheus metrics.
- `GET :9090/debug/pprof/` exposes Go profiles. Keep the diagnostics listener on
  a private network in production.

The default Compose environment serves Prometheus on
`http://localhost:9091` and Grafana on `http://localhost:3000`.

## Initial secrets and authentication

Generate a fresh local environment before the first Compose start:

```bash
go run ./cmd/hookforge generate-secrets > .env
docker compose up --build
```

The generated file contains the bootstrap API key, AES-256-GCM key, PostgreSQL
password, and Grafana password. It is excluded from Git. Back it up in a secret
manager; losing or changing the AES key makes stored endpoint signing secrets
unreadable.

All `/v1` requests require:

```text
Authorization: Bearer <tenant API key>
```

Provision an additional isolated tenant:

```bash
docker compose run --rm hookforge provision-tenant \
  --slug acme --name "Acme Corporation"
```

The command prints the tenant API key once. Store it immediately.

## Alerts

Recommended starting alerts:

- dead-letter rate is non-zero for 10 minutes;
- p95 delivery duration exceeds the configured timeout budget;
- claim errors continue for 2 minutes;
- endpoint saturation grows continuously;
- readiness fails for more than 30 seconds.

Tune thresholds against real traffic before paging.

## DLQ triage

1. Query `GET /v1/deliveries?status=dead`.
2. Inspect `last_status_code` and `last_error`.
3. Confirm the endpoint is healthy and the payload contract is still valid.
4. Replay only after correcting the receiver or endpoint configuration.
5. Watch `hookforge_delivery_attempts_total{outcome="dead"}` and the delivery
   record after replay.

Replay is at-least-once. The receiver must deduplicate the stable event ID.

## Safe shutdown

SIGTERM stops new HTTP work and new claims. In-flight webhook calls can finish
within the shutdown budget; unfinished claims become eligible after their lease
expires. Set the orchestrator termination grace period above
`HOOKFORGE_SHUTDOWN_TIMEOUT`.

## Database backup

Back up the PostgreSQL database with a tool appropriate for the deployment.
`events`, `deliveries`, and `delivery_attempts` must be restored together. Schema
migrations are forward-only and embedded in the application image.

## SSRF policy

HookForge blocks non-public endpoint networks both when an endpoint is created and
when a worker opens each connection. Redirects are not followed. Treat
`HOOKFORGE_ALLOW_PRIVATE_ENDPOINTS=true` as an unsafe local-development override;
never use it in production.
