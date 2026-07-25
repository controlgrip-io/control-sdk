from __future__ import annotations

import json

import pytest

from controlgrip_client import ControlGrip, github_source


class FakeResponse:
    def __init__(self, status_code: int = 200, body=None):
        self.status_code = status_code
        self._body = body
        self.content = b"" if body is None else json.dumps(body).encode()
        self.text = self.content.decode()

    def json(self):
        return self._body


class FakeSession:
    def __init__(self):
        self.calls: list[tuple[str, str, object]] = []

    def request(self, method: str, url: str, json=None):
        self.calls.append((method, url, json))
        path = url.removeprefix("https://example.test")
        responses = {
            ("GET", "/api/integrations/github/status"): {
                "connected": True,
                "configured": True,
                "mode": "app",
            },
            ("POST", "/api/integrations/github/connect"): {
                "authUrl": "https://github.com/login/oauth/authorize?state=signed"
            },
            (
                "GET",
                "/api/integrations/github/repositories?q=tax+agents",
            ): [{"id": 1, "owner": "acme", "name": "tax"}],
            (
                "GET",
                "/api/integrations/github/branches?owner=acme&repo=tax",
            ): [{"name": "main", "commit": {"sha": "abc"}}],
            (
                "GET",
                "/api/integrations/github/repo-file"
                "?owner=acme&repo=tax&ref=feature%2Fone&path=src%2Fjob.py",
            ): {"exists": True},
        }
        if method == "DELETE" and path == "/api/integrations/github":
            return FakeResponse(204)
        return FakeResponse(body=responses[(method, path)])


def test_github_integration_paths_and_query_encoding():
    session = FakeSession()
    cg = ControlGrip("https://example.test", session=session)

    assert cg.github_status()["connected"] is True
    assert (
        cg.connect_github()
        == "https://github.com/login/oauth/authorize?state=signed"
    )
    assert cg.github_repositories("tax agents")[0]["id"] == 1
    assert cg.github_branches("acme", "tax")[0]["name"] == "main"
    assert cg.github_repository_file_exists(
        "acme", "tax", "feature/one", "src/job.py"
    )
    assert cg.disconnect_github() is None


def test_github_source_builds_and_validates_descriptor():
    repository = {"id": 42, "owner": "acme", "name": "jobs"}

    assert github_source(
        repository,
        "main",
        "src/run.py",
        workdir="src",
        revision_policy="pin",
    ) == {
        "type": "git",
        "provider": "github",
        "authentication": "github_app",
        "repository": {"id": 42, "owner": "acme", "name": "jobs"},
        "ref": "main",
        "revision_policy": "pin",
        "workdir": "src",
        "main_path": "src/run.py",
    }

    with pytest.raises(ValueError, match="authentication"):
        github_source(repository, "main", "run.py", authentication="password")

    with pytest.raises(ValueError, match="revision_policy"):
        github_source(repository, "main", "run.py", revision_policy="latest")

    with pytest.raises(ValueError, match="missing 'id'"):
        github_source({"owner": "acme", "name": "jobs"}, "main", "run.py")
