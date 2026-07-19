/** ControlGrip API client (session-cookie auth, zero dependencies, Node 20+).
 *
 * fetch has no cookie jar, so the client captures Set-Cookie pairs from every
 * response (the sign-in sets the session) and replays them on each request.
 */

const TERMINAL_STATES = new Set(["succeeded", "failed", "cancelled"]);

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
      try {
        const err = JSON.parse(text).error;
        throw new ControlGripError(res.status, err.code ?? "?", err.message ?? text);
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
  setSecrets(jobId: string, secrets: Record<string, string>) {
    return this.put(`/api/jobs/${jobId}/secrets`, { secrets });
  }
  setVariables(jobId: string, variables: Record<string, string>) {
    return this.put(`/api/jobs/${jobId}/variables`, { variables });
  }
  updateTasks(jobId: string, tasks: unknown[], baseVersion: number) {
    return this.put(`/api/jobs/${jobId}/tasks`, { tasks, base_version: baseVersion });
  }
  async runJob(jobId: string): Promise<string> {
    const r = await this.post<{ job_run_id: string }>(`/api/jobs/${jobId}/run`);
    return r.job_run_id;
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
