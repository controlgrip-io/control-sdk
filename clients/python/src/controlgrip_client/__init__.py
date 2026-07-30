"""ControlGrip API client (session-cookie auth).

Usage:
    from controlgrip_client import ControlGrip
    cg = ControlGrip("https://www.controlgrip.io")
    cg.sign_in(email, password)
    pool = cg.ensure("/api/pools", "etl", {"name": "etl", "timezone": "UTC"})
    job = cg.create_job({...})           # see docs/cookbooks/api.md
    run_id = cg.run_job(job["id"])
    detail = cg.wait_for_run(run_id)
"""
from __future__ import annotations

import time
from typing import Any

import requests

TERMINAL_STATES = {"succeeded", "failed", "cancelled", "skipped"}


class ControlGripError(RuntimeError):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(f"{code} ({status}): {message}")
        self.status, self.code = status, code


class ControlGrip:
    def __init__(self, base_url: str, session: requests.Session | None = None):
        self.base = base_url.rstrip("/")
        self.http = session or requests.Session()

    # ── plumbing ────────────────────────────────────────────────────────────
    def request(self, method: str, path: str, json: Any | None = None) -> Any:
        r = self.http.request(method, f"{self.base}{path}", json=json)
        if r.status_code >= 300:
            # httpError emits a FLAT envelope: {"error": "<msg>", "code": "<CODE>"}.
            try:
                body = r.json()
                raise ControlGripError(r.status_code, body.get("code", "HTTP"), body.get("error", r.text))
            except ValueError:
                raise ControlGripError(r.status_code, "HTTP", r.text)
        return r.json() if r.content else None

    def get(self, path: str) -> Any: return self.request("GET", path)
    def post(self, path: str, body: Any = None) -> Any: return self.request("POST", path, body or {})
    def put(self, path: str, body: Any) -> Any: return self.request("PUT", path, body)

    # ── auth (cookie session; no API tokens yet) ────────────────────────────
    def sign_in(self, email: str, password: str) -> None:
        self.post("/api/auth/sign-in/email", {"email": email, "password": password})

    def set_active_org(self, organization_id: str) -> None:
        """Required when the automation user belongs to >1 org — the
        orchestrator only auto-resolves a single membership."""
        self.post("/api/auth/organization/set-active", {"organizationId": organization_id})

    # ── idempotent create-by-name ───────────────────────────────────────────
    def ensure(self, path: str, name: str, create_body: dict) -> dict:
        for row in self.get(path):
            if row.get("name") == name:
                return row
        return self.post(path, create_body)

    # ── domain helpers ──────────────────────────────────────────────────────
    def create_worker(self, name: str, host: str = "") -> dict:
        """Returns {worker, enrollmentKey} — the key is shown exactly once."""
        return self.post("/api/workers", {"name": name, "host": host})

    def create_job(self, body: dict) -> dict:
        return self.post("/api/jobs", body)

    def set_secrets(self, job_id: str, secrets: dict[str, str]) -> None:
        self.put(f"/api/jobs/{job_id}/secrets", {"secrets": secrets})

    def set_variables(self, job_id: str, variables: dict[str, str]) -> None:
        self.put(f"/api/jobs/{job_id}/variables", {"variables": variables})

    def update_tasks(self, job_id: str, tasks: list[dict], base_version: int) -> dict:
        return self.put(f"/api/jobs/{job_id}/tasks", {"tasks": tasks, "base_version": base_version})

    def run_job(self, job_id: str, parameters: dict | None = None) -> str:
        """Manual trigger. `parameters` is validated against the job's
        parameters_schema and read by connectors as ${var:CG_PARAM_<NAME>}."""
        body = {"parameters": parameters} if parameters is not None else {}
        return self.post(f"/api/jobs/{job_id}/run", body)["job_run_id"]

    # ── backfill: one run per past schedule window, sequentially ────────────
    def create_backfill(self, job_id: str, start: str, end: str,
                        parameters: dict | None = None) -> dict:
        """start/end are RFC3339; the job's cron defines the window
        boundaries (pinned at creation). Returns the backfill row with
        planned_runs."""
        body: dict[str, Any] = {"start": start, "end": end}
        if parameters is not None:
            body["parameters"] = parameters
        return self.post(f"/api/jobs/{job_id}/backfill", body)

    def list_backfills(self, job_id: str) -> list[dict]:
        return self.get(f"/api/jobs/{job_id}/backfills")

    def cancel_backfill(self, backfill_id: str) -> None:
        self.request("DELETE", f"/api/backfills/{backfill_id}")

    # ── await-input gates (human-in-the-loop) ───────────────────────────────
    def submit_task_input(self, run_id: str, task_key: str, input: dict) -> dict:
        """Approve an awaiting gate with typed input (validated against the
        gate's resolved schema — see run detail's await_input_schema)."""
        return self.post(f"/api/runs/{run_id}/tasks/{task_key}/input", {"input": input})

    def reject_task_input(self, run_id: str, task_key: str, reason: str) -> dict:
        """Reject an awaiting gate; the task fails and dependents block
        (a when:"fails" dependent fires instead)."""
        return self.post(f"/api/runs/{run_id}/tasks/{task_key}/input",
                         {"reject": True, "reason": reason})

    def get_run(self, run_id: str) -> dict:
        return self.get(f"/api/runs/{run_id}")

    def wait_for_run(self, run_id: str, poll_seconds: float = 5.0, timeout: float = 3600.0) -> dict:
        deadline = time.monotonic() + timeout
        while True:
            detail = self.get_run(run_id)
            if detail["state"] in TERMINAL_STATES:
                return detail
            if time.monotonic() > deadline:
                raise TimeoutError(f"run {run_id} still {detail['state']} after {timeout}s")
            time.sleep(poll_seconds)
