// Package controlgrip is a client for the ControlGrip orchestrator's
// session-scoped API (cookie auth via the auth edge; no API tokens yet).
package controlgrip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client holds the base URL and the cookie-jar HTTP client that carries the
// better-auth session across calls.
type Client struct {
	Base string
	HTTP *http.Client
}

// New builds a client with a fresh cookie jar.
func New(base string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Jar: jar}}, nil
}

// APIError is the orchestrator's {"error":{"code","message"}} envelope.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s (%d): %s", e.Code, e.Status, e.Message)
}

// Do issues a JSON request and decodes the JSON response into out (nil to discard).
func (c *Client) Do(method, path string, body, out any) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequest(method, c.Base+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		// httpError emits a FLAT envelope: {"error":"<message>","code":"<CODE>"}.
		var envelope struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.NewDecoder(res.Body).Decode(&envelope)
		if envelope.Code == "" {
			envelope.Code = "HTTP"
		}
		return &APIError{Status: res.StatusCode, Code: envelope.Code, Message: envelope.Error}
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// SignIn establishes the session cookie. Mutating endpoints additionally
// require the account to be an org owner/admin.
func (c *Client) SignIn(email, password string) error {
	return c.Do("POST", "/api/auth/sign-in/email",
		map[string]string{"email": email, "password": password}, nil)
}

// SetActiveOrg is required when the user belongs to more than one org; a
// single membership auto-resolves.
func (c *Client) SetActiveOrg(organizationID string) error {
	return c.Do("POST", "/api/auth/organization/set-active",
		map[string]string{"organizationId": organizationID}, nil)
}

// Named is the id+name projection shared by list endpoints.
type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Ensure lists path and returns the row named name, creating it via
// createBody when absent (idempotent create-by-name).
func (c *Client) Ensure(path, name string, createBody any) (Named, error) {
	var rows []Named
	if err := c.Do("GET", path, nil, &rows); err != nil {
		return Named{}, err
	}
	for _, row := range rows {
		if row.Name == name {
			return row, nil
		}
	}
	var created Named
	if err := c.Do("POST", path, createBody, &created); err != nil {
		return Named{}, err
	}
	return created, nil
}

// CreateWorkerResult carries the ONE-TIME enrollment key.
type CreateWorkerResult struct {
	Worker        Named  `json:"worker"`
	EnrollmentKey string `json:"enrollmentKey"`
}

func (c *Client) CreateWorker(name, host string) (CreateWorkerResult, error) {
	var out CreateWorkerResult
	err := c.Do("POST", "/api/workers", map[string]any{"name": name, "host": host}, &out)
	return out, err
}

func (c *Client) CreateJob(body any) (Named, error) {
	var out Named
	err := c.Do("POST", "/api/jobs", body, &out)
	return out, err
}

func (c *Client) SetSecrets(jobID string, secrets map[string]string) error {
	return c.Do("PUT", "/api/jobs/"+jobID+"/secrets", map[string]any{"secrets": secrets}, nil)
}

func (c *Client) SetVariables(jobID string, variables map[string]string) error {
	return c.Do("PUT", "/api/jobs/"+jobID+"/variables", map[string]any{"variables": variables}, nil)
}

func (c *Client) UpdateTasks(jobID string, tasks []any, baseVersion int) error {
	return c.Do("PUT", "/api/jobs/"+jobID+"/tasks",
		map[string]any{"tasks": tasks, "base_version": baseVersion}, nil)
}

func (c *Client) RunJob(jobID string) (string, error) {
	var out struct {
		JobRunID string `json:"job_run_id"`
	}
	err := c.Do("POST", "/api/jobs/"+jobID+"/run", map[string]any{}, &out)
	return out.JobRunID, err
}

// RunDetail is a minimal projection of GET /api/runs/{id}.
type RunDetail struct {
	State            string `json:"state"`
	ValidationStatus string `json:"validation_status"`
}

// GitHubIntegrationStatus describes whether the server is configured and the
// current organization has linked a GitHub App installation.
type GitHubIntegrationStatus struct {
	Connected  bool   `json:"connected"`
	Configured bool   `json:"configured"`
	Mode       string `json:"mode"`
	Login      string `json:"login"`
	AvatarURL  string `json:"avatarUrl"`
	ProfileURL string `json:"profileUrl"`
}

// GitHubRepository is one repository in the organization-scoped grant
// captured when the GitHub App installation was connected.
type GitHubRepository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"fullName"`
	Owner         string `json:"owner"`
	DefaultBranch string `json:"defaultBranch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"htmlUrl"`
}

// GitHubBranch is a branch and its current commit.
type GitHubBranch struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// GitHubStatus returns the current organization's integration status.
func (c *Client) GitHubStatus() (GitHubIntegrationStatus, error) {
	var out GitHubIntegrationStatus
	err := c.Do("GET", "/api/integrations/github/status", nil, &out)
	return out, err
}

// ConnectGitHub starts the owner/admin-only connection flow and returns the
// GitHub URL that a user must open in a browser to authorize and install the
// App.
func (c *Client) ConnectGitHub(returnTo string) (string, error) {
	if strings.TrimSpace(returnTo) == "" {
		returnTo = "/settings/integrations"
	}
	var out struct {
		AuthURL string `json:"authUrl"`
	}
	err := c.Do("POST", "/api/integrations/github/connect",
		map[string]string{"returnTo": returnTo}, &out)
	return out.AuthURL, err
}

// DisconnectGitHub removes the organization connection. The API returns
// GITHUB_IN_USE while a current source binding or active run still needs it.
func (c *Client) DisconnectGitHub() error {
	return c.Do("DELETE", "/api/integrations/github", nil, nil)
}

// GitHubRepositories lists the recorded repository grant. query is an
// optional case-insensitive substring filter on fullName.
func (c *Client) GitHubRepositories(query string) ([]GitHubRepository, error) {
	values := url.Values{}
	if query = strings.TrimSpace(query); query != "" {
		values.Set("q", query)
	}
	path := "/api/integrations/github/repositories"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out []GitHubRepository
	err := c.Do("GET", path, nil, &out)
	return out, err
}

// GitHubBranches lists branches for a repository in the recorded grant.
func (c *Client) GitHubBranches(owner, repo string) ([]GitHubBranch, error) {
	values := url.Values{"owner": {owner}, "repo": {repo}}
	var out []GitHubBranch
	err := c.Do("GET", "/api/integrations/github/branches?"+values.Encode(), nil, &out)
	return out, err
}

// GitHubRepositoryFileExists checks a repository-relative path at ref.
func (c *Client) GitHubRepositoryFileExists(owner, repo, ref, path string) (bool, error) {
	values := url.Values{
		"owner": {owner},
		"repo":  {repo},
		"ref":   {ref},
		"path":  {path},
	}
	var out struct {
		Exists bool `json:"exists"`
	}
	err := c.Do("GET", "/api/integrations/github/repo-file?"+values.Encode(), nil, &out)
	return out.Exists, err
}

func (c *Client) GetRun(runID string) (RunDetail, error) {
	var out RunDetail
	err := c.Do("GET", "/api/runs/"+runID, nil, &out)
	return out, err
}

// WaitForRun polls until the run reaches a terminal state.
func (c *Client) WaitForRun(runID string, poll, timeout time.Duration) (RunDetail, error) {
	deadline := time.Now().Add(timeout)
	for {
		detail, err := c.GetRun(runID)
		if err != nil {
			return detail, err
		}
		switch detail.State {
		case "succeeded", "failed", "cancelled":
			return detail, nil
		}
		if time.Now().After(deadline) {
			return detail, fmt.Errorf("run %s still %s after %s", runID, detail.State, timeout)
		}
		time.Sleep(poll)
	}
}
