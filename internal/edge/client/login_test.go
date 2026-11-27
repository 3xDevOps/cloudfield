package edgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// An edge that predates the relay origin field decodes the request into
// the fields it knows; a newer one reads the relay origin the client
// signs in for. Both start the sign-in.
func TestDeviceStartSendsRelayOrigin(t *testing.T) {
	for _, newer := range []bool{false, true} {
		edge := newFakeEdge(t)
		var got map[string]any
		edge.mux.HandleFunc("POST "+edgeproto.PathDeviceStart, func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil || json.Unmarshal(raw, &got) != nil {
				writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: "bad start"})
				return
			}
			var older struct {
				Label string `json:"label"`
				Key   string `json:"key"`
			}
			var req edgeproto.DeviceStartRequest
			if newer {
				err = json.Unmarshal(raw, &req)
			} else {
				err = json.Unmarshal(raw, &older)
				req = edgeproto.DeviceStartRequest{Label: older.Label, Key: older.Key}
			}
			if err != nil || req.Validate() != nil || (newer && req.RelayOrigin != edge.URL) {
				writeJSON(w, http.StatusBadRequest, edgeproto.ErrorBody{Error: "bad start"})
				return
			}
			writeJSON(w, http.StatusOK, edgeproto.DeviceStartResponse{
				DeviceCode: "device-code", UserCode: "ABCD-EFGH", VerificationURI: edge.URL + "/device", ExpiresIn: 60,
			})
		})
		c := newTestClient(t, edge)
		if _, err := c.StartLogin(context.Background(), "laptop"); err != nil {
			t.Fatalf("newer edge %v: %v", newer, err)
		}
		if got["relay_origin"] != edge.URL {
			t.Fatalf("newer edge %v: relay_origin = %v, want %s", newer, got["relay_origin"], edge.URL)
		}
	}
}

// A retired address answers a sign-out with 410 and the replacement it
// names: the token is deleted here and reported as not revoked. Every
// other refusal keeps the token, except the 401 of a token the edge
// already refuses.
func TestLogoutAtRetiredAddress(t *testing.T) {
	const retired = `{"error":"https://old.example.test is retired; this edge is at https://new.example.test","retired":{"replacement":"https://new.example.test"}}`
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		deleted   bool
		unrevoked *Unrevoked
		noVersion bool
	}{
		{"revoked", http.StatusNoContent, "", true, nil, false},
		{"already refused", http.StatusUnauthorized, `{"error":"token revoked"}`, true, nil, false},
		{"retired", http.StatusGone, retired, true, &Unrevoked{Device: edgeproto.Device{ID: "dev-1", Label: "laptop"}, Replacement: "https://new.example.test"}, false},
		{"gone without retired", http.StatusGone, `{"error":"gone"}`, false, nil, false},
		{"retired names no origin", http.StatusGone, `{"error":"gone","retired":{"replacement":"https://new.example.test/login?code=1"}}`, false, nil, false},
		{"retired names a control character", http.StatusGone, `{"error":"gone","retired":{"replacement":"https://new.example.test\u001b[2J"}}`, false, nil, false},
		{"retired on another status", http.StatusForbidden, retired, false, nil, false},
		{"server error", http.StatusInternalServerError, `{"error":"boom"}`, false, nil, false},
		{"retired without the edge's version", http.StatusGone, retired, false, nil, true},
		{"retired in a body that does not decode", http.StatusGone, retired + "}", false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edge := newFakeEdge(t)
			edge.mux.HandleFunc("POST "+edgeproto.PathLogout, func(w http.ResponseWriter, r *http.Request) {
				if !tc.noVersion {
					w.Header().Set(edgeproto.HeaderVersion, "1")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			c := newTestClient(t, edge)
			signIn(t, c)
			unrevoked, err := c.Logout(context.Background())
			_, sessionErr := c.Session()
			if deleted := errors.Is(sessionErr, ErrNotSignedIn); deleted != tc.deleted {
				t.Fatalf("token deleted = %v, want %v (logout error %v)", deleted, tc.deleted, err)
			}
			if tc.deleted != (err == nil) {
				t.Fatalf("Logout error = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "the token is still valid and kept in") {
				t.Fatalf("Logout error = %v, want it to say the token is kept", err)
			}
			if tc.unrevoked == nil {
				if unrevoked != nil {
					t.Fatalf("Unrevoked = %+v, want none", unrevoked)
				}
				return
			}
			if unrevoked == nil || unrevoked.Device.ID != tc.unrevoked.Device.ID || unrevoked.Replacement != tc.unrevoked.Replacement {
				t.Fatalf("Unrevoked = %+v, want %+v", unrevoked, tc.unrevoked)
			}
		})
	}
}

// A refused request's error keeps its form on a retired address.
func TestRetiredRefusalText(t *testing.T) {
	edge := newFakeEdge(t)
	edge.mux.HandleFunc("GET "+edgeproto.PathServers, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(edgeproto.HeaderVersion, "1")
		w.WriteHeader(http.StatusGone)
		_, _ = io.WriteString(w, `{"error":"retired here","retired":{"replacement":"https://new.example.test"}}`)
	})
	c := newTestClient(t, edge)
	signIn(t, c)
	_, err := c.Servers(context.Background())
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Retired != "https://new.example.test" {
		t.Fatalf("Servers error = %v, want a retired refusal", err)
	}
	if want := "list servers: " + c.Host() + " refused: retired here (HTTP 410)"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}
