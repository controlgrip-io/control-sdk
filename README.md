# control-sdk

Client SDKs for the [ControlGrip](https://www.controlgrip.io) orchestrator API
— the platform for deterministic AI-agent jobs: cron-scheduled jobs of ordered
tasks, connectors, per-task output contracts validated deterministically, and
bounded AI remediation.

## Layout

```
openapi.yaml           API sketch (session-scoped orchestrator surface)
clients/python/        controlgrip-client  (Python ≥3.10, requests)
clients/typescript/    @controlgrip/client (Node 20+, zero-dependency fetch)
clients/go/            Go module           (stdlib net/http + cookiejar)
clients/rust/          controlgrip-client  (Rust 2021, reqwest)
```

## Auth model

The API is session-scoped: sign in with `POST /api/auth/sign-in/email` and the
client carries the session cookie on every call (each client handles cookies
idiomatically; the TypeScript client captures `Set-Cookie` manually since
`fetch` has no jar). Reads need org membership; **mutations need an org
owner/admin**. There are no API tokens yet — use a dedicated automation
account (without 2FA) for scripting.

## Shared surface

Every client exposes the same operations:

| Operation | Notes |
|---|---|
| `sign_in(email, password)` | establishes the cookie session |
| `ensure(path, name, body)` | idempotent list-then-create-by-name |
| `create_worker(name, host)` | response carries the **one-time** enrollment key |
| `create_job(body)` | tasks + `depends_on` DAG + `expected_output` contracts + optional inline cron |
| `set_secrets(job_id, {...})` | write-only server-side; referenced as `${secret:NAME}` |
| `set_variables(job_id, {...})` | plain values; referenced as `${var:NAME}` |
| `update_tasks(job_id, tasks, base_version)` | publishes a new job version (optimistic concurrency) |
| `run_job(job_id)` → run id | manual trigger |
| `get_run(id)` / `wait_for_run(id)` | state, validation results, poll to terminal |

## Quickstart (Python; the other clients mirror it)

```python
from controlgrip_client import ControlGrip

cg = ControlGrip("https://www.controlgrip.io")
cg.sign_in(EMAIL, PASSWORD)

pool = cg.ensure("/api/pools", "etl", {"name": "etl", "timezone": "UTC"})
conn = cg.ensure("/api/connectors", "my-source", {
    "name": "my-source", "type": "controlgrip-python",
    "config": {"entrypoint": "read"}})

job = cg.create_job({
    "name": "users-sync", "project_name": "acme", "pool_id": pool["id"],
    "tasks": [{
        "key": "sync", "name": "Sync users", "position": 0,
        "connector_ref": conn["id"], "action": "read",
        "input": {"since": "${var:SINCE}"},
        "expected_output": {"checks": [{"type": "record_count", "min": 1}]},
    }],
    "schedule": {"cron": "0 2 * * *", "timezone": "UTC"},
})
cg.set_secrets(job["id"], {"API_KEY": "..."})
cg.set_variables(job["id"], {"SINCE": "2026-01-01"})

detail = cg.wait_for_run(cg.run_job(job["id"]))
print(detail["state"], detail["validation_status"])
```

Contract validators available in `expected_output.checks`: `record_count`,
`stream_record_count`, `required_streams`, `fields_present`,
`field_aggregate`. Dependency triggers: `passes` (terminal **and** validated),
`finishes`, `starts`.

## Status

v0.1, source-distributed (no registries yet). Errors surface the API's
`{"error":{"code","message"}}` envelope as typed errors in each language.

## License

Apache-2.0
