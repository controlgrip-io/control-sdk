// Package controlgrip is a client for the ControlGrip orchestrator's
// session-scoped API (cookie auth via the auth edge; no API tokens yet).
package controlgrip

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
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
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&envelope)
		if envelope.Error.Code == "" {
			envelope.Error.Code = "HTTP"
		}
		return &APIError{Status: res.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
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
	err := c.Do("POST", path, createBody, &created)
	return created, err
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
