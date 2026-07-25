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

#[derive(Debug, Deserialize)]
pub struct GitHubIntegrationStatus {
    pub connected: bool,
    pub configured: bool,
    pub mode: String,
    #[serde(default)]
    pub login: String,
    #[serde(default, rename = "avatarUrl")]
    pub avatar_url: String,
    #[serde(default, rename = "profileUrl")]
    pub profile_url: String,
}

#[derive(Debug, Deserialize)]
pub struct GitHubRepository {
    pub id: i64,
    pub name: String,
    #[serde(rename = "fullName")]
    pub full_name: String,
    pub owner: String,
    #[serde(rename = "defaultBranch")]
    pub default_branch: String,
    pub private: bool,
    #[serde(rename = "htmlUrl")]
    pub html_url: String,
}

#[derive(Debug, Deserialize)]
pub struct GitHubCommit {
    pub sha: String,
}

#[derive(Debug, Deserialize)]
pub struct GitHubBranch {
    pub name: String,
    pub commit: GitHubCommit,
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
            "DELETE" => self.http.delete(&url),
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

    pub fn github_status(&self) -> Result<GitHubIntegrationStatus, Error> {
        let value = self.request("GET", "/api/integrations/github/status", None)?;
        Ok(serde_json::from_value(value).expect("GitHub integration status"))
    }

    /// Start the owner/admin connection flow. The returned GitHub URL must be
    /// opened in a browser to complete user authorization and App installation.
    pub fn connect_github(&self, return_to: &str) -> Result<String, Error> {
        let return_to = if return_to.trim().is_empty() {
            "/settings/integrations"
        } else {
            return_to
        };
        let value = self.request(
            "POST",
            "/api/integrations/github/connect",
            Some(json!({"returnTo": return_to})),
        )?;
        Ok(value["authUrl"].as_str().unwrap_or_default().to_string())
    }

    pub fn disconnect_github(&self) -> Result<(), Error> {
        self.request("DELETE", "/api/integrations/github", None)?;
        Ok(())
    }

    pub fn github_repositories(&self, query: &str) -> Result<Vec<GitHubRepository>, Error> {
        let path = if query.trim().is_empty() {
            "/api/integrations/github/repositories".to_string()
        } else {
            format!(
                "/api/integrations/github/repositories{}",
                encode_query(&[("q", query.trim())])
            )
        };
        let value = self.request("GET", &path, None)?;
        Ok(serde_json::from_value(value).expect("GitHub repositories"))
    }

    pub fn github_branches(&self, owner: &str, repo: &str) -> Result<Vec<GitHubBranch>, Error> {
        let path = format!(
            "/api/integrations/github/branches{}",
            encode_query(&[("owner", owner), ("repo", repo)])
        );
        let value = self.request("GET", &path, None)?;
        Ok(serde_json::from_value(value).expect("GitHub branches"))
    }

    pub fn github_repository_file_exists(
        &self,
        owner: &str,
        repo: &str,
        git_ref: &str,
        path: &str,
    ) -> Result<bool, Error> {
        let endpoint = format!(
            "/api/integrations/github/repo-file{}",
            encode_query(&[
                ("owner", owner),
                ("repo", repo),
                ("ref", git_ref),
                ("path", path),
            ])
        );
        let value = self.request("GET", &endpoint, None)?;
        Ok(value["exists"].as_bool().unwrap_or(false))
    }

    pub fn run_job(&self, job_id: &str) -> Result<String, Error> {
        let v = self.request("POST", &format!("/api/jobs/{job_id}/run"), Some(json!({})))?;
        Ok(v["job_run_id"].as_str().unwrap_or_default().to_string())
    }

    pub fn get_run(&self, run_id: &str) -> Result<RunDetail, Error> {
        let v = self.request("GET", &format!("/api/runs/{run_id}"), None)?;
        Ok(serde_json::from_value(v).expect("run detail"))
    }

    pub fn wait_for_run(&self, run_id: &str, poll: Duration, timeout: Duration) -> Result<RunDetail, Error> {
        let deadline = Instant::now() + timeout;
        loop {
            let detail = self.get_run(run_id)?;
            if matches!(detail.state.as_str(), "succeeded" | "failed" | "cancelled") {
                return Ok(detail);
            }
            if Instant::now() > deadline {
                return Err(Error::Timeout(run_id.to_string(), detail.state));
            }
            thread::sleep(poll);
        }
    }
}

fn encode_query(params: &[(&str, &str)]) -> String {
    let mut url =
        reqwest::Url::parse("https://controlgrip.invalid").expect("static query base URL");
    url.query_pairs_mut().extend_pairs(params.iter().copied());
    format!("?{}", url.query().unwrap_or_default())
}

#[cfg(test)]
mod tests {
    use super::encode_query;

    #[test]
    fn github_query_values_are_encoded() {
        assert_eq!(
            encode_query(&[
                ("owner", "acme"),
                ("repo", "tax agents"),
                ("ref", "feature/one"),
                ("path", "src/job.py"),
            ]),
            "?owner=acme&repo=tax+agents&ref=feature%2Fone&path=src%2Fjob.py",
        );
    }
}
