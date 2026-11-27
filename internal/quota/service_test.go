package quota

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

// A member signed in through the Anthropic Console is billed by the API:
// usage says so, and neither calls them signed out nor asks the vendor.
func TestReadReportsAConsoleSignInAsAPIBilling(t *testing.T) {
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected request to %s", req.URL)
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	for name, tc := range map[string]struct {
		login      string
		files      map[string]string
		wantStatus string
		want       string
	}{
		"API key": {
			files: map[string]string{".claude.json": `{"primaryApiKey":"fixture-key"}`},
			want:  "Anthropic Console sign-in (~/.claude.json) is billed by the API and has no usage windows",
		},
		"API key beside a signed-out login file": {
			login: `{"claudeAiOauth":{"accessToken":"","refreshToken":""}}`,
			files: map[string]string{".claude.json": `{"primaryApiKey":"fixture-key"}`},
			want:  "Anthropic Console sign-in (~/.claude.json) is billed by the API and has no usage windows",
		},
		"API key beside a login file that is not JSON": {
			login: `not json`,
			files: map[string]string{".claude.json": `{"primaryApiKey":"fixture-key"}`},
			want:  "Anthropic Console sign-in (~/.claude.json) is billed by the API and has no usage windows",
		},
		"profile": {
			files: map[string]string{
				".config/anthropic/configs/default.json":     `{"authentication":{"type":"user_oauth"}}`,
				".config/anthropic/credentials/default.json": `{"access_token":"fixture-access"}`,
			},
			want: "Anthropic Console sign-in (~/.config/anthropic) is billed by the API and has no usage windows",
		},
		// Claude Code renews an expired login that has a refresh token, so
		// the subscription is still the login in use.
		"API key beside a login the agent renews": {
			login:      `{"claudeAiOauth":{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresAt":1}}`,
			files:      map[string]string{".claude.json": `{"primaryApiKey":"fixture-key"}`},
			wantStatus: "unauthenticated",
			want:       "Claude OAuth credential is expired",
		},
		"sign-in the server cannot read": {
			files:      map[string]string{".claude.json": `not json`},
			wantStatus: "unauthenticated",
			want:       "Claude OAuth credential not found; a sign-in outside the login file cannot be read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			homes, member := fixtureHomes(t, tc.login, "")
			home, err := homes.Path(member)
			if err != nil {
				t.Fatal(err)
			}
			for rel, body := range tc.files {
				if err = os.MkdirAll(filepath.Dir(filepath.Join(home, rel)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(home, rel), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			providers, err := New(homes, nil).Read(context.Background(), member, false)
			if err != nil {
				t.Fatal(err)
			}
			claude := providers[0]
			if claude.Provider != "claude" || claude.Status != cmp.Or(tc.wantStatus, "unsupported") || claude.Error != tc.want || len(claude.Windows) != 0 {
				t.Fatalf("Claude provider = %#v, want %s with %q", claude, cmp.Or(tc.wantStatus, "unsupported"), tc.want)
			}
		})
	}
}

func TestReadCollectsClaudeAndCodexWithoutLeakingCredentialBodies(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"claude-secret","expiresAt":4102444800000,"scopes":["user:inference"],"subscriptionType":"max"}}`, `{"tokens":{"access_token":"codex-secret","account_id":"acct-1"}}`)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		if req.URL.String() == claudeUsageEndpoint {
			if got := req.Header.Get("Authorization"); got != "Bearer claude-secret" {
				return nil, fmt.Errorf("Claude auth header = %q", got)
			}
			if req.Header.Get("anthropic-beta") != "oauth-2025-04-20" || req.Header.Get("User-Agent") != "claude-code/2.1.0" {
				return nil, errors.New("missing Claude usage headers")
			}
			return fixtureResponse(`{"five_hour":{"utilization":12.5,"resets_at":"2100-01-01T00:00:00Z"},"seven_day":{"utilization":50,"resets_at":"2100-01-02T00:00:00Z"},"seven_day_sonnet":{"used_percentage":2,"resets_at":null}}`), nil
		}
		if req.URL.String() == codexUsageEndpoint {
			if req.Header.Get("Authorization") != "Bearer codex-secret" || req.Header.Get("ChatGPT-Account-Id") != "acct-1" {
				return nil, errors.New("missing Codex auth headers")
			}
			if req.Header.Get("User-Agent") != "codex-cli" || req.Header.Get("OpenAI-Beta") != "codex-1" || req.Header.Get("originator") != "Codex Desktop" {
				return nil, errors.New("missing Codex usage headers")
			}
			return fixtureResponse(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":30,"limit_window_seconds":1800,"reset_at":2100000000},"secondary_window":{"used_percent":4,"limit_window_seconds":604800,"reset_at":2100000100}}}`), nil
		}
		return nil, fmt.Errorf("unexpected URL %s", req.URL)
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes, nil)
	providers, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(providers))
	}
	if providers[0].Provider != "claude" || providers[0].Status != "ok" || providers[0].Plan != "max" || len(providers[0].Windows) != 3 {
		t.Fatalf("Claude provider = %#v", providers[0])
	}
	if providers[0].Windows[0].UsedPercent != 12.5 || providers[0].Windows[2].UsedPercent != 2 || providers[0].Windows[2].ResetsAt != nil {
		t.Fatalf("Claude windows = %#v", providers[0].Windows)
	}
	if providers[1].Provider != "codex" || providers[1].Status != "ok" || providers[1].Plan != "plus" || len(providers[1].Windows) != 2 {
		t.Fatalf("Codex provider = %#v", providers[1])
	}
	for i, want := range []string{"30 minutes", "7 days"} {
		window := providers[1].Windows[i]
		if window.Label != want {
			t.Errorf("Codex window %s label = %q, want %q", window.ID, window.Label, want)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	providers[0].Windows[0].UsedPercent = 99
	cached, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if cached[0].Windows[0].UsedPercent != 12.5 {
		t.Fatalf("cached result was mutable through caller: %#v", cached[0].Windows)
	}
}

func TestReadHonorsIdentityChangeAndDoesNotServeOldCache(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"old-token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var authHeaders []string
	var mu sync.Mutex
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		authHeaders = append(authHeaders, req.Header.Get("Authorization"))
		mu.Unlock()
		return fixtureResponse(`{"five_hour":{"utilization":10,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes, nil)
	if _, err := service.Read(context.Background(), member, false); err != nil {
		t.Fatal(err)
	}
	home, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, claudeCredentialFile), []byte(`{"claudeAiOauth":{"accessToken":"new-token","expiresAt":4102444800000,"scopes":["user:inference"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := service.Read(context.Background(), member, false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(authHeaders) != 2 || authHeaders[0] != "Bearer old-token" || authHeaders[1] != "Bearer new-token" {
		t.Fatalf("auth headers = %#v", authHeaders)
	}
}

func TestReadStaleDropsWindowsAfterResetAndHonorsRetryAfter(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch requests.Add(1) {
		case 1:
			return fixtureResponse(`{"five_hour":{"utilization":25,"resets_at":"2026-09-18T12:05:00Z"}}`), nil
		default:
			return fixtureResponseWithStatus(http.StatusTooManyRequests, `secret provider body`, http.Header{"Retry-After": []string{"120"}}), nil
		}
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	current := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	service := New(homes, nil)
	service.now = func() time.Time { return current }
	if got, err := service.Read(context.Background(), member, false); err != nil || got[0].Status != "ok" {
		t.Fatalf("initial read = %#v, %v", got, err)
	}
	current = current.Add(6 * time.Minute)
	got, err := service.Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "stale" || len(got[0].Windows) != 0 || got[0].RetryAt == nil {
		t.Fatalf("stale read = %#v", got[0])
	}
	if strings.Contains(got[0].Error, "secret") {
		t.Fatalf("provider body leaked in error: %q", got[0].Error)
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d, want 2", requests.Load())
	}
	retryAt := *got[0].RetryAt
	safeError := got[0].Error
	current = retryAt.Add(-time.Second)
	later, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if later[0].Status != "stale" || later[0].Error != safeError || later[0].RetryAt == nil || !later[0].RetryAt.Equal(retryAt) {
		t.Fatalf("stale retry result = %#v, want status/error/retry_at preserved", later[0])
	}
	if requests.Load() != 2 {
		t.Fatalf("retry cooldown did not suppress request: %d", requests.Load())
	}
}

func TestSafeHTTPErrorHonorsRetryAfter503(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	status, _, retryAt := classifyFetchError(safeHTTPError(http.StatusServiceUnavailable, http.Header{"Retry-After": []string{"120"}}, now), now)
	if status != "error" || !retryAt.Equal(now.Add(120*time.Second)) {
		t.Fatalf("503 classification = (%q, %v), want error and 120-second retry", status, retryAt)
	}
}

func TestReadPreservesInitialFailureDuringRetryCooldown(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return fixtureResponseWithStatus(http.StatusUnauthorized, `secret provider body`, nil), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	current := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	service := New(homes, nil)
	service.now = func() time.Time { return current }
	first, err := service.Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Status != "unauthenticated" || first[0].RetryAt == nil {
		t.Fatalf("initial failure = %#v", first[0])
	}
	retryAt := *first[0].RetryAt
	safeError := first[0].Error
	current = retryAt.Add(-time.Second)
	second, err := service.Read(context.Background(), member, false)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Status != "unauthenticated" || second[0].Error != safeError || second[0].RetryAt == nil || !second[0].RetryAt.Equal(retryAt) {
		t.Fatalf("cooldown failure = %#v, want persisted failure", second[0])
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want one request", requests.Load())
	}
}

func TestReadRejectsQuotaWithNoValidWindows(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return fixtureResponse(`{"five_hour":{"utilization":null,"resets_at":null},"seven_day":{"utilization":101,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	got, err := New(homes, nil).Read(context.Background(), member, true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "error" || len(got[0].Windows) != 0 {
		t.Fatalf("invalid quota result = %#v", got[0])
	}
}

func TestCredentialParsingRecognizesAPIKeysScopesAndMalformedJSON(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"Claude API key", []byte(`{"apiKey":"api-key"}`), "unsupported"},
		{"Claude scope", []byte(`{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:profile"]}}`), "unauthenticated"},
		{"Claude malformed", []byte(`{"claudeAiOauth":{"accessToken":"super-secret"`), "error"},
		{"Codex API key", []byte(`{"OPENAI_API_KEY":"api-key"}`), "unsupported"},
		{"Codex missing account", []byte(`{"tokens":{"access_token":"token"}}`), "unauthenticated"},
		// An account share leaves an empty mountpoint file in a launcher's
		// home that never logged in.
		{"Claude empty mountpoint", []byte{}, "unauthenticated"},
		{"Codex empty mountpoint", []byte{}, "unauthenticated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got credential
			if strings.HasPrefix(tc.name, "Claude") {
				got = parseClaudeCredential(tc.data, nil, now)
			} else {
				got = parseCodexCredential(tc.data, nil, now)
			}
			if got.status != tc.want {
				t.Fatalf("status = %q, want %q", got.status, tc.want)
			}
			if strings.Contains(got.err, "api-key") || strings.Contains(got.err, "token") || strings.Contains(got.err, "super-secret") {
				t.Fatalf("credential secret leaked in error: %q", got.err)
			}
		})
	}
}

func TestReadCoalescesRequestsAndCanceledLeaderDoesNotCancelFlight(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var requests atomic.Int32
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		return fixtureResponse(`{"five_hour":{"utilization":20,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })

	service := New(homes, nil)
	leaderCtx, cancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := service.Read(leaderCtx, member, true)
		leaderDone <- err
	}()
	<-started
	followerDone := make(chan error, 1)
	go func() {
		_, err := service.Read(context.Background(), member, true)
		followerDone <- err
	}()
	for range 1000 {
		service.mu.Lock()
		joined := service.flight[string(member)] != nil
		service.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(release)
	if err := <-leaderDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want context cancellation", err)
	}
	if err := <-followerDone; err != nil {
		t.Fatalf("follower error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want one coalesced request", requests.Load())
	}
}

func fixtureHomes(t *testing.T, claudeJSON, codexJSON string) (*memberhome.Manager, domain.MemberID) {
	t.Helper()
	root := t.TempDir()
	homes, err := memberhome.New(filepath.Join(root, "homes"), filepath.Join(root, "home-caches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	member := domain.MemberID("member-1")
	home, err := homes.Path(member)
	if err != nil {
		t.Fatal(err)
	}
	if claudeJSON != "" {
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, claudeCredentialFile), []byte(claudeJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if codexJSON != "" {
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, codexCredentialFile), []byte(codexJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return homes, member
}

func fixtureResponse(body string) *http.Response {
	return fixtureResponseWithStatus(http.StatusOK, body, nil)
}

func fixtureResponseWithStatus(status int, body string, headers http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// A read asked for fresh does not take the answer of a read in flight that
// may have come from the cache.
func TestAFreshReadDoesNotJoinACachedOne(t *testing.T) {
	homes, member := fixtureHomes(t, `{"claudeAiOauth":{"accessToken":"token","expiresAt":4102444800000,"scopes":["user:inference"]}}`, ``)
	oldTransport := usageHTTPClient.Transport
	usageHTTPClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return fixtureResponse(`{"five_hour":{"utilization":20,"resets_at":"2100-01-01T00:00:00Z"}}`), nil
	})
	t.Cleanup(func() { usageHTTPClient.Transport = oldTransport })
	service := New(homes, nil)
	cached := &readFlight{done: make(chan struct{}), providers: []Provider{{Provider: "claude", Status: "ok", Windows: []Window{{ID: "five_hour", UsedPercent: 96}}}}}
	service.mu.Lock()
	service.flight[string(member)] = cached
	service.mu.Unlock()
	defer close(cached.done)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := service.Read(ctx, member, true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "ok" || got[0].Windows[0].UsedPercent != 20 {
		t.Fatalf("fresh read = %+v; want the vendor's 20%%, not the read in flight", got[0])
	}
}
