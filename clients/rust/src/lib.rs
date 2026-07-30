//! ControlGrip orchestrator API client (session-cookie auth via the auth
//! edge; no API tokens yet). Mutations require an org owner/admin session.

use reqwest::blocking::Client as Http;
use serde::Deserialize;
use serde_json::{json, Value};
use std::{thread, time::Duration, time::Instant};

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("{code} ({status}): {message}")]
    Api { status: u16, code: String, message: String },
    #[error(transparent)]
    Http(#[from] reqwest::Error),
    #[error("run {0} still {1} after timeout")]
    Timeout(String, String),
}

#[derive(Debug, Clone, Deserialize)]
pub struct Named {
    pub id: String,
    #[serde(default)]
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct CreateWorkerResult {
    pub worker: Named,
    /// Shown exactly once — persist it before dropping the response.
    #[serde(rename = "enrollmentKey")]
    pub enrollment_key: String,
}

#[derive(Debug, Deserialize)]
pub struct RunDetail {
    pub state: String,
    #[serde(default)]
    pub validation_status: String,
}

pub struct ControlGrip {
    base: String,
    http: Http,
}

impl ControlGrip {
    pub fn new(base: &str) -> Result<Self, Error> {
        Ok(Self {
            base: base.trim_end_matches('/').to_string(),
            // The cookie store carries the better-auth session across calls.
            http: Http::builder().cookie_store(true).build()?,
        })
    }

    pub fn request(&self, method: &str, path: &str, body: Option<Value>) -> Result<Value, Error> {
        let url = format!("{}{}", self.base, path);
        let req = match method {
            "GET" => self.http.get(&url),
            "POST" => self.http.post(&url),
            "PUT" => self.http.put(&url),
            _ => unreachable!("unsupported method {method}"),
        };
        let req = match body {
            Some(b) => req.json(&b),
            None => req,
        };
        let res = req.send()?;
        let status = res.status();
        let text = res.text()?;
        if !status.is_success() {
            // httpError emits a FLAT envelope: {"error":"<msg>","code":"<CODE>"}.
            let envelope: Value = serde_json::from_str(&text).unwrap_or(Value::Null);
            return Err(Error::Api {
                status: status.as_u16(),
                code: envelope["code"].as_str().unwrap_or("HTTP").to_string(),
                message: envelope["error"].as_str().unwrap_or(&text).to_string(),
            });
        }
        Ok(serde_json::from_str(&text).unwrap_or(Value::Null))
    }

    pub fn sign_in(&self, email: &str, password: &str) -> Result<(), Error> {
        self.request("POST", "/api/auth/sign-in/email",
            Some(json!({"email": email, "password": password})))?;
        Ok(())
    }

    /// Required when the user belongs to more than one org (single membership
    /// auto-resolves).
    pub fn set_active_org(&self, organization_id: &str) -> Result<(), Error> {
        self.request("POST", "/api/auth/organization/set-active",
            Some(json!({"organizationId": organization_id})))?;
        Ok(())
    }

    /// Idempotent create-by-name: list `path`, return the row named `name`,
    /// else POST `create_body`.
    pub fn ensure(&self, path: &str, name: &str, create_body: Value) -> Result<Named, Error> {
        let rows = self.request("GET", path, None)?;
        if let Some(hit) = rows.as_array().into_iter().flatten().find(|r| r["name"] == name) {
            return Ok(serde_json::from_value(hit.clone()).expect("id+name projection"));
        }
        let created = self.request("POST", path, Some(create_body))?;
        Ok(serde_json::from_value(created).expect("id+name projection"))
    }

    pub fn create_worker(&self, name: &str, host: &str) -> Result<CreateWorkerResult, Error> {
        let v = self.request("POST", "/api/workers", Some(json!({"name": name, "host": host})))?;
        Ok(serde_json::from_value(v).expect("createWorker response"))
    }

    pub fn create_job(&self, body: Value) -> Result<Named, Error> {
        let v = self.request("POST", "/api/jobs", Some(body))?;
        Ok(serde_json::from_value(v).expect("job response"))
    }

    pub fn set_secrets(&self, job_id: &str, secrets: Value) -> Result<(), Error> {
        self.request("PUT", &format!("/api/jobs/{job_id}/secrets"), Some(json!({"secrets": secrets})))?;
        Ok(())
    }

    pub fn set_variables(&self, job_id: &str, variables: Value) -> Result<(), Error> {
        self.request("PUT", &format!("/api/jobs/{job_id}/variables"), Some(json!({"variables": variables})))?;
        Ok(())
    }

    pub fn run_job(&self, job_id: &str) -> Result<String, Error> {
        self.run_job_with(job_id, None)
    }

    /// Manual trigger with typed run parameters, validated against the job's
    /// parameters_schema and read by connectors as `${var:CG_PARAM_<NAME>}`.
    pub fn run_job_with(&self, job_id: &str, parameters: Option<Value>) -> Result<String, Error> {
        let body = match parameters {
            Some(p) => json!({ "parameters": p }),
            None => json!({}),
        };
        let v = self.request("POST", &format!("/api/jobs/{job_id}/run"), Some(body))?;
        Ok(v["job_run_id"].as_str().unwrap_or_default().to_string())
    }

    /// One run per past schedule window in [start, end), sequentially;
    /// start/end are RFC3339 and the job's cron defines the boundaries.
    pub fn create_backfill(
        &self, job_id: &str, start: &str, end: &str, parameters: Option<Value>,
    ) -> Result<Value, Error> {
        let mut body = json!({ "start": start, "end": end });
        if let Some(p) = parameters {
            body["parameters"] = p;
        }
        self.request("POST", &format!("/api/jobs/{job_id}/backfill"), Some(body))
    }

    pub fn list_backfills(&self, job_id: &str) -> Result<Value, Error> {
        self.request("GET", &format!("/api/jobs/{job_id}/backfills"), None)
    }

    pub fn cancel_backfill(&self, backfill_id: &str) -> Result<(), Error> {
        self.request("DELETE", &format!("/api/backfills/{backfill_id}"), None)?;
        Ok(())
    }

    /// Approve an awaiting gate with typed input (validated against the
    /// gate's resolved schema — run detail's `await_input_schema`).
    pub fn submit_task_input(&self, run_id: &str, task_key: &str, input: Value) -> Result<Value, Error> {
        self.request("POST", &format!("/api/runs/{run_id}/tasks/{task_key}/input"),
            Some(json!({ "input": input })))
    }

    /// Reject an awaiting gate; the task fails and dependents block
    /// (a `when:"fails"` dependent fires instead).
    pub fn reject_task_input(&self, run_id: &str, task_key: &str, reason: &str) -> Result<Value, Error> {
        self.request("POST", &format!("/api/runs/{run_id}/tasks/{task_key}/input"),
            Some(json!({ "reject": true, "reason": reason })))
    }

    pub fn get_run(&self, run_id: &str) -> Result<RunDetail, Error> {
        let v = self.request("GET", &format!("/api/runs/{run_id}"), None)?;
        Ok(serde_json::from_value(v).expect("run detail"))
    }

    pub fn wait_for_run(&self, run_id: &str, poll: Duration, timeout: Duration) -> Result<RunDetail, Error> {
        let deadline = Instant::now() + timeout;
        loop {
            let detail = self.get_run(run_id)?;
            if matches!(detail.state.as_str(), "succeeded" | "failed" | "cancelled" | "skipped") {
                return Ok(detail);
            }
            if Instant::now() > deadline {
                return Err(Error::Timeout(run_id.to_string(), detail.state));
            }
            thread::sleep(poll);
        }
    }
}
