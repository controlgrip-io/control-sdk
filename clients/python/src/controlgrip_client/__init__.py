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

TERMINAL_STATES = {"succeeded", "failed", "cancelled"}


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
            try:
                err = r.json()["error"]
                raise ControlGripError(r.status_code, err.get("code", "?"), err.get("message", r.text))
            except (ValueError, KeyError):
                raise ControlGripError(r.status_code, "HTTP", r.text)
        return r.json() if r.content else None

    def get(self, path: str) -> Any: return self.request("GET", path)
    def post(self, path: str, body: Any = None) -> Any: return self.request("POST", path, body or {})
    def put(self, path: str, body: Any) -> Any: return self.request("PUT", path, body)

    # ── auth (cookie session; no API tokens yet) ────────────────────────────
    def sign_in(self, email: str, password: str) -> None:
        self.post("/api/auth/sign-in/email", {"email": email, "password": password})

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

    def run_job(self, job_id: str) -> str:
        return self.post(f"/api/jobs/{job_id}/run")["job_run_id"]

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
