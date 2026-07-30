package controlgrip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitHubIntegrationMethods(t *testing.T) {
	var connectReturnTo string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/integrations/github/status":
			_, _ = w.Write([]byte(`{"connected":true,"configured":true,"mode":"app","login":"acme"}`))
		case r.Method == "POST" && r.URL.Path == "/api/integrations/github/connect":
			var body struct {
				ReturnTo string `json:"returnTo"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			connectReturnTo = body.ReturnTo
			_, _ = w.Write([]byte(`{"authUrl":"https://github.com/authorize"}`))
		case r.Method == "GET" && r.URL.Path == "/api/integrations/github/repositories":
			if got := r.URL.Query().Get("q"); got != "tax agents" {
				t.Errorf("q = %q", got)
			}
			_, _ = w.Write([]byte(`[{"id":1,"name":"jobs","fullName":"acme/jobs","owner":"acme","defaultBranch":"main","private":true,"htmlUrl":"https://github.com/acme/jobs"}]`))
		case r.Method == "GET" && r.URL.Path == "/api/integrations/github/branches":
			if r.URL.Query().Get("owner") != "acme" || r.URL.Query().Get("repo") != "jobs" {
				t.Errorf("unexpected branch query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"name":"main","commit":{"sha":"abc"}}]`))
		case r.Method == "GET" && r.URL.Path == "/api/integrations/github/repo-file":
			if r.URL.Query().Get("ref") != "feature/one" || r.URL.Query().Get("path") != "src/job.py" {
				t.Errorf("unexpected file query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"exists":true}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/integrations/github":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GitHubStatus()
	if err != nil || !status.Connected || status.Login != "acme" {
		t.Fatalf("status = %#v, err = %v", status, err)
	}
	authURL, err := client.ConnectGitHub("")
	if err != nil || authURL != "https://github.com/authorize" {
		t.Fatalf("authURL = %q, err = %v", authURL, err)
	}
	if connectReturnTo != "/settings/integrations" {
		t.Fatalf("returnTo = %q", connectReturnTo)
	}
	repositories, err := client.GitHubRepositories(" tax agents ")
	if err != nil || len(repositories) != 1 || repositories[0].ID != 1 {
		t.Fatalf("repositories = %#v, err = %v", repositories, err)
	}
	branches, err := client.GitHubBranches("acme", "jobs")
	if err != nil || len(branches) != 1 || branches[0].Commit.SHA != "abc" {
		t.Fatalf("branches = %#v, err = %v", branches, err)
	}
	exists, err := client.GitHubRepositoryFileExists(
		"acme", "jobs", "feature/one", "src/job.py")
	if err != nil || !exists {
		t.Fatalf("exists = %v, err = %v", exists, err)
	}
	if err := client.DisconnectGitHub(); err != nil {
		t.Fatal(err)
	}
}
