package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/cli"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/testhome"
)

const (
	oldEdge = "https://old.example.test"
	newEdge = "https://new.example.test"
)

func TestRetiringNotice(t *testing.T) {
	const (
		noDate   = "cloudfield: https://old.example.test says it is retiring and that its new address is https://new.example.test.\n"
		withDate = "cloudfield: https://old.example.test says it is retiring on 2027-03-01 and that its new address is https://new.example.test.\n"
		confirm  = "Confirm that address with whoever runs the edge before you sign in there.\n"
		login    = "Sign in there with: cloudfield login --edge https://new.example.test\n"
		guide    = "Moving links and the sync daemon, and signing out of https://old.example.test: " + movingGuide + "\n"
	)
	at := func(links ...cli.NamedLink) cli.Config { return cli.Config{Links: links} }
	for _, tc := range []struct {
		name     string
		notice   edgeclient.Retiring
		holdings cli.EdgeHoldings
		daemon   string
		read     bool
		want     string
	}{
		{"unknown, no date", edgeclient.Retiring{Replacement: newEdge}, cli.EdgeHoldings{SignedIn: []string{oldEdge}}, "", true,
			noDate + confirm + guide},
		{"unknown, with date", edgeclient.Retiring{Replacement: newEdge, Date: "2027-03-01"}, cli.EdgeHoldings{SignedIn: []string{oldEdge}}, "", true,
			withDate + confirm + guide},
		{"unknown, default link and daemon at the old address", edgeclient.Retiring{Replacement: newEdge},
			cli.EdgeHoldings{SignedIn: []string{oldEdge}, Config: cli.Config{EdgeURL: oldEdge, ServerID: testServerID}}, oldEdge + ":443", true,
			noDate + confirm + "The default link (server " + testServerID + ") still names https://old.example.test.\n" +
				"The sync daemon's unit names https://old.example.test.\n" + guide},
		{"holdings unreadable", edgeclient.Retiring{Replacement: newEdge}, cli.EdgeHoldings{}, "", false,
			noDate + confirm + guide},
		{"token at the new address", edgeclient.Retiring{Replacement: newEdge}, cli.EdgeHoldings{SignedIn: []string{oldEdge, newEdge}}, "", true,
			noDate + guide},
		{"token at another spelling of the new address", edgeclient.Retiring{Replacement: newEdge}, cli.EdgeHoldings{SignedIn: []string{oldEdge, newEdge + ":443"}}, "", true,
			noDate + login + guide},
		{"a named link at the new address, another at the old", edgeclient.Retiring{Replacement: newEdge},
			cli.EdgeHoldings{SignedIn: []string{oldEdge}, Config: at(
				cli.NamedLink{Name: "work box", Addr: "10.0.0.5:2222", EdgeURL: oldEdge + ":443", ServerID: testServerID},
				cli.NamedLink{Name: "other", EdgeURL: newEdge, ServerID: "aaaaaaaaaaaaaaaaaaaaaaaaaa"},
				cli.NamedLink{Name: "ssh only", Addr: "plain:2222"},
			)}, "", true,
			noDate + login + "The link \"work box\" (server " + testServerID + ") still names https://old.example.test.\n" + guide},
		{"the built-in default named by a self-hosted address", edgeclient.Retiring{Replacement: edgeclient.DefaultURL}, cli.EdgeHoldings{SignedIn: []string{oldEdge}}, "", true,
			"cloudfield: https://old.example.test says it is retiring and that its new address is " + edgeclient.DefaultURL + ".\n" + confirm + guide},
		{"daemon unit at another address", edgeclient.Retiring{Replacement: newEdge}, cli.EdgeHoldings{SignedIn: []string{newEdge}}, "https://other.example.test", true,
			noDate + guide},
		{"replacement is the old address", edgeclient.Retiring{Replacement: oldEdge}, cli.EdgeHoldings{SignedIn: []string{oldEdge}}, "", true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retiringNotice(oldEdge, tc.notice, tc.holdings, tc.daemon, tc.read); got != tc.want {
				t.Errorf("notice:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

// The notice for a hosted address offers sign-in at the built-in edge.
func TestRetiringNoticeFromAHostedAddress(t *testing.T) {
	got := retiringNotice(edgeclient.FormerURL, edgeclient.Retiring{Replacement: edgeclient.DefaultURL}, cli.EdgeHoldings{SignedIn: []string{edgeclient.FormerURL}}, "", true)
	if want := "Sign in there with: cloudfield login --edge " + edgeclient.DefaultURL + "\n"; !strings.Contains(got, want) {
		t.Fatalf("notice:\n%s\nwant it to contain %q", got, want)
	}
}

// retiringEdge is an edge that signs devices in, lists no servers and
// revokes tokens, and puts value in Aether-Edge-Retiring on every answer.
func retiringEdge(t *testing.T, value string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var edge *httptest.Server
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set(edgeproto.HeaderVersion, "1")
		if value != "" {
			w.Header().Set(edgeproto.HeaderRetiring, value)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	var key string
	mux.HandleFunc("GET "+edgeproto.PathEdgeInfo, func(w http.ResponseWriter, _ *http.Request) {
		pub := make([]byte, 32)
		reply(w, edgeproto.EdgeInfo{SigninOrigin: edge.URL, Key: pub, Fingerprint: edgeproto.EdgeKeyFingerprint(pub),
			Version: edgeproto.Version, MinVersion: edgeproto.MinVersion})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
		var req edgeproto.DeviceStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		key = req.Key
		reply(w, edgeproto.DeviceStartResponse{DeviceCode: "dc", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", ExpiresIn: 60, Interval: 1})
	})
	mux.HandleFunc("POST "+edgeproto.PathDeviceToken, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, edgeproto.DeviceTokenResponse{Token: edgeproto.NewToken(), Device: edgeproto.Device{ID: "dev-1", Label: "laptop", Key: key},
			Account: edgeproto.AccountInfo{ID: edgeproto.NewAccountID(), Account: edgeproto.Account{Provider: edgeproto.ProviderGitHub, Subject: "1", Login: "octo"}}})
	})
	mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, _ *http.Request) {
		reply(w, edgeproto.ServersResponse{})
	})
	edge = httptest.NewServer(mux)
	t.Cleanup(edge.Close)
	return edge
}

// writeEdgeTokens stores a device token for each relay origin, issued
// at the sign-in origin it maps to.
func writeEdgeTokens(t *testing.T, signin map[string]string) string {
	t.Helper()
	dir, err := cli.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	edges := map[string]any{}
	for relay, origin := range signin {
		edges[relay] = map[string]any{"token": edgeproto.NewToken(), "signin_origin": origin, "device": map[string]any{"id": "dev-1", "label": "laptop"}}
	}
	raw, err := json.Marshal(map[string]any{"edges": edges})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, edgeclient.TokensFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// seenAt is the notice this run saw for origin alone, as main prints it.
func seenAt(origin string) map[string]edgeclient.Retiring {
	out := map[string]edgeclient.Retiring{}
	if r, ok := edgeclient.Seen()[origin]; ok {
		out[origin] = r
	}
	return out
}

// A command that talked to a retiring edge leaves stdout as it was and
// prints the notice on stderr; an edge without a notice, or with a value
// that is not a canonical origin and a real date, printable and short,
// prints nothing.
func TestRetiringNoticeAfterACommand(t *testing.T) {
	long := "https://" + strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + "." + strings.Repeat("d", 60) + ".example.test"
	for _, tc := range []struct {
		name, value, want string
	}{
		{"no notice", "", ""},
		{"retiring", newEdge, "says it is retiring and that its new address is https://new.example.test.\n" +
			"Confirm that address with whoever runs the edge before you sign in there.\n"},
		{"retiring with a date", newEdge + "; date=2027-03-01", "says it is retiring on 2027-03-01 and that its new address is https://new.example.test.\n"},
		{"not an origin", "new.example.test", ""},
		{"a path", newEdge + "/login?code=1", ""},
		{"a tab", "https://new\t.example.test", ""},
		{"a bidi override", "https://new.example\u202e.test", ""},
		{"very long", long, ""},
		{"a date that is not real", newEdge + "; date=2027-02-30", ""},
		{"a date that is not a date", newEdge + "; date=soon", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testhome.Isolate(t)
			edge := retiringEdge(t, tc.value)
			writeEdgeTokens(t, map[string]string{edge.URL: edge.URL})
			out, err := captureStdout(t, func() error { return dispatch([]string{"servers", "--edge", edge.URL}) })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out, "no servers yet on ") || strings.Contains(out, "retiring") {
				t.Fatalf("stdout = %q", out)
			}
			var stderr bytes.Buffer
			printRetiring(&stderr, seenAt(edge.URL))
			if tc.want == "" {
				if stderr.Len() != 0 {
					t.Fatalf("stderr = %q, want nothing", stderr.String())
				}
				return
			}
			if !strings.HasPrefix(stderr.String(), "cloudfield: "+edge.URL+" ") || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), tc.want)
			}
		})
	}
}

// login --json prints its two JSON lines and nothing else on stdout.
func TestRetiringNoticeLeavesJSONAlone(t *testing.T) {
	testhome.Isolate(t)
	edge := retiringEdge(t, newEdge)
	out, err := captureStdout(t, func() error { return dispatch([]string{"login", "--edge", edge.URL, "--label", "laptop", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout = %q, want two lines", out)
	}
	for _, line := range lines {
		var v loginLine
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("stdout line %q: %v", line, err)
		}
	}
	var stderr bytes.Buffer
	printRetiring(&stderr, seenAt(edge.URL))
	if !strings.Contains(stderr.String(), "its new address is https://new.example.test.") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

// logout at an address that answers it is retired deletes the token and
// says the edge did not confirm it, naming a Devices page only for a
// replacement this machine knows; every other refusal but 401 keeps it.
// The relay and sign-in hosts differ, as on a hosted edge: sign-out goes
// to the sign-in host, and its refusal names that host.
func TestLogoutAtARetiredAddress(t *testing.T) {
	const (
		signedOut = "signed out of RELAY: the device token is revoked at the edge and deleted here; the device key and your links stay\n"
		retired   = `{"error":"retired","retired":{"replacement":"https://new.example.test"}}`
		notified  = "deleted the device token for RELAY here, but the edge did not confirm the sign-out: it answered that this address is retired and that its new address is https://new.example.test.\n"
	)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		atNew  bool
		stdout string
		errMsg string
	}{
		{"revoked", http.StatusNoContent, "", false, signedOut, ""},
		{"already refused", http.StatusUnauthorized, `{"error":"token revoked"}`, false, signedOut, ""},
		{"retired, replacement unknown", http.StatusGone, retired, false,
			notified + "Device dev-1 may still be signed in; ask whoever runs the edge for the address of its Devices page and revoke it there.\n", ""},
		{"retired, signed in at the replacement", http.StatusGone, retired, true,
			notified + "Device dev-1 may still be signed in; revoke it on https://auth.new.example.test/devices.\n" +
				"still signed in to new.example.test; sign out there with: aether logout --edge https://new.example.test\n", ""},
		{"gone without retired", http.StatusGone, `{"error":"gone"}`, false, "", "sign out: SIGNIN refused: gone (HTTP 410); the token is still valid and kept in "},
		{"forbidden", http.StatusForbidden, `{"error":"no"}`, false, "", "sign out: SIGNIN refused: no (HTTP 403); the token is still valid and kept in "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testhome.Isolate(t)
			signin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(edgeproto.HeaderVersion, "1")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(signin.Close)
			relay := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(relay.Close)
			tokens := map[string]string{relay.URL: signin.URL}
			if tc.atNew {
				tokens[newEdge] = "https://auth.new.example.test"
			}
			dir := writeEdgeTokens(t, tokens)
			hosts := strings.NewReplacer("RELAY", strings.TrimPrefix(relay.URL, "http://"), "SIGNIN", strings.TrimPrefix(signin.URL, "http://"))
			out, err := captureStdout(t, func() error { return dispatch([]string{"logout", "--edge", relay.URL}) })
			signedIn, readErr := edgeclient.SignedIn(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if slices.Contains(signedIn, relay.URL) == (tc.errMsg == "") {
				t.Fatalf("token at the retired address kept = %v, want %v", !(tc.errMsg == ""), tc.errMsg != "")
			}
			if tc.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), hosts.Replace(tc.errMsg)) {
					t.Fatalf("error = %v, want %q", err, hosts.Replace(tc.errMsg))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := hosts.Replace(tc.stdout); out != want {
				t.Fatalf("stdout:\n%s\nwant:\n%s", out, want)
			}
		})
	}
}
