package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recorded struct {
	auth  string
	query string
	vars  map[string]any
}

func fakeLinear(t *testing.T, status int, reply string) (*Client, *recorded) {
	t.Helper()
	got := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("request body: %v", err)
		}
		got.auth, got.query, got.vars = r.Header.Get("Authorization"), req.Query, req.Variables
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL, HTTP: srv.Client()}, got
}

func TestViewerSendsTheKeyWithoutBearer(t *testing.T) {
	c, got := fakeLinear(t, 200, `{"data":{"viewer":{"name":"Ada","organization":{"name":"Acme","urlKey":"acme"}}}}`)
	st, err := c.Viewer(context.Background(), "lin_api_x")
	if err != nil {
		t.Fatal(err)
	}
	if got.auth != "lin_api_x" {
		t.Errorf("Authorization = %q, want the bare key", got.auth)
	}
	if want := (Status{Connected: true, Name: "Ada", Organization: "Acme", URLKey: "acme"}); st != want {
		t.Errorf("status = %+v, want %+v", st, want)
	}
}

func TestErrorsArrayWinsOverStatus(t *testing.T) {
	for _, status := range []int{200, 400} {
		c, _ := fakeLinear(t, status, `{"data":null,"errors":[{"message":"Authentication required"},{"message":"second"}]}`)
		_, err := c.Teams(context.Background(), "bad")
		if err == nil || !strings.Contains(err.Error(), "Authentication required; second") {
			t.Errorf("status %d: err = %v, want Linear's messages", status, err)
		}
	}
}

func TestRefusedKeyIsMarkedAndTransportFailureIsNot(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		reply  string
	}{
		"authentication message": {400, `{"errors":[{"message":"Authentication required, not authenticated"}]}`},
		"authentication type":    {200, `{"errors":[{"message":"nope","extensions":{"type":"authentication error"}}]}`},
		"403 status":             {403, "denied"},
		"401 status":             {401, "{}"},
	} {
		c, _ := fakeLinear(t, tc.status, tc.reply)
		_, err := c.Teams(context.Background(), "bad")
		if err == nil || !strings.HasPrefix(err.Error(), RefusedKey) {
			t.Errorf("%s: err = %v, want prefix %q", name, err, RefusedKey)
		}
	}
	c, _ := fakeLinear(t, 502, "bad gateway")
	if _, err := c.Teams(context.Background(), "k"); err == nil || strings.HasPrefix(err.Error(), RefusedKey) {
		t.Errorf("502: err = %v, want no refusal marker", err)
	}
	c.URL = "http://127.0.0.1:1"
	if _, err := c.Teams(context.Background(), "k"); err == nil || strings.HasPrefix(err.Error(), RefusedKey) {
		t.Errorf("transport: err = %v, want no refusal marker", err)
	}
}

func TestNonJSONFailureNamesTheStatus(t *testing.T) {
	c, _ := fakeLinear(t, 502, "bad gateway")
	_, err := c.Teams(context.Background(), "k")
	if err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "bad gateway") {
		t.Errorf("err = %v, want the status and body", err)
	}
}

func TestTeams(t *testing.T) {
	c, _ := fakeLinear(t, 200, `{"data":{"teams":{"nodes":[{"key":"CLO","name":"Cloudfield"}]}}}`)
	teams, err := c.Teams(context.Background(), "k")
	if err != nil || len(teams) != 1 || teams[0].Key != "CLO" || teams[0].Name != "Cloudfield" {
		t.Errorf("teams = %+v, %v", teams, err)
	}
}

func TestIssuesFilterAndFields(t *testing.T) {
	c, got := fakeLinear(t, 200, `{"data":{"viewer":{"assignedIssues":{"nodes":[
		{"identifier":"CLO-6","title":"Linear","url":"https://linear.app/a/issue/CLO-6","state":{"name":"In Progress","type":"started","color":"#f2c94c"},"team":{"key":"CLO","name":"Cloudfield"},"assignee":{"name":"Ada"},"updatedAt":"2026-01-02T03:04:05Z"}]}}}}`)
	issues, err := c.Issues(context.Background(), "k", IssuesFilter{Team: "CLO", Query: " lin "})
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Identifier != "CLO-6" || issues[0].State.Type != "started" ||
		issues[0].Team.Key != "CLO" || issues[0].Assignee.Name != "Ada" || issues[0].UpdatedAt != "2026-01-02T03:04:05Z" {
		t.Errorf("issues = %+v", issues)
	}
	for _, want := range []string{"assignedIssues", "first: 50", "orderBy: updatedAt", "updatedAt"} {
		if !strings.Contains(got.query, want) {
			t.Errorf("query lacks %q: %s", want, got.query)
		}
	}
	raw, _ := json.Marshal(got.vars["filter"])
	for _, want := range []string{`"nin":["completed","canceled"]`, `"eqIgnoreCase":"CLO"`, `"containsIgnoreCase":"lin"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("filter lacks %s: %s", want, raw)
		}
	}

	if _, err := c.Issues(context.Background(), "k", IssuesFilter{}); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(got.vars["filter"])
	if strings.Contains(string(raw), "team") || strings.Contains(string(raw), "title") {
		t.Errorf("unfiltered request still narrows: %s", raw)
	}
}

func TestIssueByIdentifier(t *testing.T) {
	c, got := fakeLinear(t, 200, `{"data":{"issue":{"identifier":"CLO-6","title":"Linear","url":"u","state":{"name":"Todo"}}}}`)
	issue, err := c.Issue(context.Background(), "k", "CLO-6")
	if err != nil || issue.Identifier != "CLO-6" || issue.State.Name != "Todo" || issue.Team != nil {
		t.Errorf("issue = %+v, %v", issue, err)
	}
	if got.vars["id"] != "CLO-6" {
		t.Errorf("variables = %v", got.vars)
	}
}

func TestKeysAreOwnerOnlyFilesPerMember(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "linear")
	k := NewKeys(dir)
	if _, err := k.Get("m1"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("Get before Put = %v, want ErrNotConnected", err)
	}
	if err := k.Put("m1", "key-1"); err != nil {
		t.Fatal(err)
	}
	if err := k.Put("m2", "key-2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.Get("m1"); got != "key-1" {
		t.Errorf("m1 key = %q", got)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "m1.key"): 0o600} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s = %v, %v; want %v", path, fi, err, want)
		}
	}
	if err := k.Delete("m1"); err != nil {
		t.Fatal(err)
	}
	if err := k.Delete("m1"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("second Delete = %v, want ErrNotConnected", err)
	}
	if got, _ := k.Get("m2"); got != "key-2" {
		t.Errorf("m2 key = %q after deleting m1", got)
	}
	if err := k.Put("../escape", "x"); err == nil {
		t.Error("Put accepted a path in the member id")
	}
}

func TestServiceKeepsNothingWhenLinearRefusesTheKey(t *testing.T) {
	c, _ := fakeLinear(t, 400, `{"errors":[{"message":"Authentication required"}]}`)
	s := &Service{Keys: NewKeys(t.TempDir()), Client: c}
	if _, err := s.Connect(context.Background(), "m1", "bad"); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("Connect = %v", err)
	}
	if _, err := s.Keys.Get("m1"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("a refused key was saved: %v", err)
	}
	if err := s.Disconnect("m1"); err != nil {
		t.Errorf("Disconnect without a key = %v, want nil", err)
	}
}
