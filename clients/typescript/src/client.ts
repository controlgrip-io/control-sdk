/** ControlGrip API client (session-cookie auth, zero dependencies, Node 20+).
 *
 * fetch has no cookie jar, so the client captures Set-Cookie pairs from every
 * response (the sign-in sets the session) and replays them on each request.
 */

const TERMINAL_STATES = new Set(["succeeded", "failed", "cancelled", "skipped"]);

export class ControlGripError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(`${code} (${status}): ${message}`);
  }
}

export class ControlGrip {
  private cookies = new Map<string, string>();

  constructor(private base: string) {
    this.base = base.replace(/\/$/, "");
  }

  // ── plumbing ──────────────────────────────────────────────────────────────
  async request<T = unknown>(method: string, path: string, body?: unknown): Promise<T> {
    const res = await fetch(`${this.base}${path}`, {
      method,
      headers: {
        "content-type": "application/json",
        ...(this.cookies.size
          ? { cookie: [...this.cookies.values()].join("; ") }
          : {}),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    for (const raw of res.headers.getSetCookie?.() ?? []) {
      const pair = raw.split(";")[0];
      this.cookies.set(pair.slice(0, pair.indexOf("=")), pair);
    }
    const text = await res.text();
    if (!res.ok) {
      // httpError emits a FLAT envelope: {"error":"<msg>","code":"<CODE>"}.
      try {
        const body = JSON.parse(text) as { error?: string; code?: string };
        throw new ControlGripError(res.status, body.code ?? "HTTP", body.error ?? text);
      } catch (e) {
        if (e instanceof ControlGripError) throw e;
        throw new ControlGripError(res.status, "HTTP", text);
      }
    }
    return (text ? JSON.parse(text) : undefined) as T;
  }

  get<T = unknown>(path: string) { return this.request<T>("GET", path); }
  post<T = unknown>(path: string, body: unknown = {}) { return this.request<T>("POST", path, body); }
  put<T = unknown>(path: string, body: unknown) { return this.request<T>("PUT", path, body); }

  // ── auth (cookie session; no API tokens yet) ─────────────────────────────
  signIn(email: string, password: string) {
    return this.post("/api/auth/sign-in/email", { email, password });
  }

  /** Required when the user belongs to >1 org (single membership auto-resolves). */
  setActiveOrg(organizationId: string) {
    return this.post("/api/auth/organization/set-active", { organizationId });
  }

  // ── idempotent create-by-name ─────────────────────────────────────────────
  async ensure<T extends { name?: string }>(path: string, name: string, createBody: unknown): Promise<T> {
    const rows = await this.get<T[]>(path);
    return rows.find((r) => r.name === name) ?? (await this.post<T>(path, createBody));
  }

  // ── domain helpers ────────────────────────────────────────────────────────
  /** Response carries the ONE-TIME enrollmentKey. */
  createWorker(name: string, host = "") {
    return this.post<{ worker: { id: string }; enrollmentKey: string }>(
      "/api/workers", { name, host });
  }
  createJob(body: unknown) { return this.post<{ id: string }>("/api/jobs", body); }
  /** Organization-scoped, write-only; referenced as ${secret:NAME}.
   * (The per-job endpoints this SDK originally called were removed.) */
  setSecrets(secrets: Record<string, string>) {
    return this.put("/api/organization/secrets", { secrets });
  }
  /** Organization-scoped plain values; referenced as ${var:NAME}. */
  setVariables(variables: Record<string, string>, remove?: string[]) {
    return this.put("/api/organization/variables", { variables, ...(remove ? { remove } : {}) });
  }
  updateTasks(jobId: string, tasks: unknown[], baseVersion: number) {
    return this.put(`/api/jobs/${jobId}/tasks`, { tasks, base_version: baseVersion });
  }
  /** Manual trigger. `parameters` is validated against the job's
   * parameters_schema and read by connectors as ${var:CG_PARAM_<NAME>}. */
  async runJob(jobId: string, parameters?: Record<string, unknown>): Promise<string> {
    const r = await this.post<{ job_run_id: string }>(
      `/api/jobs/${jobId}/run`, parameters ? { parameters } : {});
    return r.job_run_id;
  }
  /** One run per past schedule window, sequentially; start/end RFC3339. */
  createBackfill(jobId: string, start: string, end: string, parameters?: Record<string, unknown>) {
    return this.post<{ id: string; planned_runs: number; state: string }>(
      `/api/jobs/${jobId}/backfill`, { start, end, ...(parameters ? { parameters } : {}) });
  }
  listBackfills(jobId: string) {
    return this.get<Array<{ id: string; state: string; runs_started: number }>>(
      `/api/jobs/${jobId}/backfills`);
  }
  cancelBackfill(backfillId: string) {
    return this.request("DELETE", `/api/backfills/${backfillId}`);
  }
  /** Approve an awaiting gate with typed input (see await_input_schema on run detail). */
  submitTaskInput(runId: string, taskKey: string, input: Record<string, unknown>) {
    return this.post(`/api/runs/${runId}/tasks/${taskKey}/input`, { input });
  }
  /** Reject an awaiting gate; the task fails (a when:"fails" dependent fires instead). */
  rejectTaskInput(runId: string, taskKey: string, reason: string) {
    return this.post(`/api/runs/${runId}/tasks/${taskKey}/input`, { reject: true, reason });
  }
  getRun(runId: string) {
    return this.get<{ state: string; validation_status?: string }>(`/api/runs/${runId}`);
  }
  async waitForRun(runId: string, pollMs = 5000, timeoutMs = 3_600_000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const detail = await this.getRun(runId);
      if (TERMINAL_STATES.has(detail.state)) return detail;
      if (Date.now() > deadline) throw new Error(`run ${runId} still ${detail.state}`);
      await new Promise((r) => setTimeout(r, pollMs));
    }
  }
}
