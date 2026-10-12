package hub_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"grok-bridge/internal/grokacp"
	"grok-bridge/internal/selfcheck"
)

func TestAdminResetRequiresAuth(t *testing.T) {
	ts, srv, _ := testServer(t)
	called := false
	srv.ResetAgent = func(context.Context) grokacp.ResetResult { called = true; return grokacp.ResetResult{} }
	for _, hdr := range []string{"", "Bearer wrong-token"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/reset", nil)
		if hdr != "" {
			req.Header.Set("Authorization", hdr)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("auth=%q: want 401, got %d", hdr, res.StatusCode)
		}
	}
	if called {
		t.Fatal("reset ran without auth")
	}
}

func TestAdminResetReturnsChecks(t *testing.T) {
	ts, srv, _ := testServer(t)
	srv.ResetAgent = func(context.Context) grokacp.ResetResult {
		return grokacp.ResetResult{StoppedPID: 11, AgentPID: 42, Connected: true}
	}
	srv.SelfCheck = func(context.Context) []selfcheck.Check {
		return []selfcheck.Check{
			{Name: "grok_binary", OK: true, Detail: "/x/grok"},
			{Name: "grok_auth", OK: true, Detail: "logged in"},
		}
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/admin/reset", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: want 405, got %d", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/admin/reset", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	var body struct {
		OK       bool              `json:"ok"`
		Checks   []selfcheck.Check `json:"checks"`
		AgentPID int               `json:"agentPid"`
		Health   map[string]bool   `json:"health"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.AgentPID != 42 || len(body.Checks) != 2 || !body.Health["hub"] || !body.Health["agent"] {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestAdminResetNotOKOnFatalCheck(t *testing.T) {
	ts, srv, _ := testServer(t)
	srv.ResetAgent = func(context.Context) grokacp.ResetResult {
		return grokacp.ResetResult{Connected: false, Error: "grok binary not found on PATH"}
	}
	srv.SelfCheck = func(context.Context) []selfcheck.Check {
		return []selfcheck.Check{{Name: "grok_binary", OK: false, Fatal: true}}
	}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/admin/reset", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body["ok"] != false || body["error"] == nil {
		t.Fatalf("want ok=false with error, got %+v", body)
	}
}
