package edgeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Notice states: a retiring relay origin still works; a retired one
// answers nothing but its refusal.
const (
	NoticeRetiring = "retiring"
	NoticeRetired  = "retired"
)

// HeaderRetiring is on every answer of a retiring address pair:
// FormatRetiring's value, the replacement relay origin with an optional
// date.
const HeaderRetiring = "Aether-Edge-Retiring"

const noticesContext = "aether-edge-notices-v1\x00"

// Notice is one entry of Ready.Notices: the relay origin Origin is in
// State, and Replacement is the relay origin of the same edge to use
// instead. Date, "YYYY-MM-DD" or empty, is text for people.
type Notice struct {
	State       string `json:"state"`
	Origin      string `json:"origin"`
	Replacement string `json:"replacement"`
	Date        string `json:"date,omitempty"`
}

// Notices decodes leniently, so a later edge's notices never refuse the
// ready they came in: an entry that does not decode or validate is
// dropped, and a list of more than MaxRelayOrigins entries is dropped
// whole. A list that lost an entry no longer matches its signature.
type Notices []Notice

// UnmarshalJSON keeps the valid entries of data and never fails. It
// decodes one entry at a time and stops at the first past the cap, so a
// long list costs no more than a full one.
func (n *Notices) UnmarshalJSON(data []byte) error {
	*n = nil
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil
	}
	var out Notices
	for i := 0; dec.More(); i++ {
		var raw json.RawMessage
		if i == MaxRelayOrigins || dec.Decode(&raw) != nil {
			return nil
		}
		var x Notice
		if json.Unmarshal(raw, &x) == nil && x.validate() == nil {
			out = append(out, x)
		}
	}
	*n = out
	return nil
}

// OptionalBytes is a base64 field a peer may leave out: a value that is
// not a base64 string decodes as absent, never as an error.
type OptionalBytes []byte

// UnmarshalJSON decodes data, or nothing when it is not a base64 string.
func (b *OptionalBytes) UnmarshalJSON(data []byte) error {
	var v []byte
	*b = nil
	if len(data) > 0 && data[0] == '"' && json.Unmarshal(data, &v) == nil {
		*b = v
	}
	return nil
}

func (n Notice) validate() error {
	switch {
	case n.State != NoticeRetiring && n.State != NoticeRetired:
		return fmt.Errorf("unknown notice state %q", n.State)
	case !canonical(n.Origin) || !canonical(n.Replacement):
		return fmt.Errorf("notice origins %q and %q must be canonical edge origins", n.Origin, n.Replacement)
	case n.Origin == n.Replacement:
		return fmt.Errorf("notice for %s names itself as the replacement", n.Origin)
	case n.Date != "" && !ValidDate(n.Date):
		return fmt.Errorf("notice date %q is not a YYYY-MM-DD date", n.Date)
	}
	return nil
}

func canonical(origin string) bool {
	o, err := Origin(origin)
	return err == nil && o == origin
}

// ValidDate reports whether s is a real date written YYYY-MM-DD.
func ValidDate(s string) bool {
	t, err := time.Parse(time.DateOnly, s)
	return err == nil && t.Format(time.DateOnly) == s
}

// noticesMessage is what the edge key signs for a notice list:
// "aether-edge-notices-v1\x00" || server id || "\x00" || origin || "\x00"
// || nonce, then for each notice state || "\x00" || origin || "\x00" ||
// replacement || "\x00" || date || "\x00". The nonce has a fixed size and
// no other field can hold a NUL byte.
func noticesMessage(serverID, origin string, nonce []byte, notices []Notice) []byte {
	msg := []byte(noticesContext)
	msg = append(msg, serverID...)
	msg = append(msg, 0)
	msg = append(msg, origin...)
	msg = append(msg, 0)
	msg = append(msg, nonce...)
	for _, n := range notices {
		for _, f := range []string{n.State, n.Origin, n.Replacement, n.Date} {
			msg = append(msg, f...)
			msg = append(msg, 0)
		}
	}
	return msg
}

// SignNotices signs notices, an empty list included, with the edge key
// for the server serverID, which dialed origin and sent nonce as
// Hello.NoticeNonce. The result is Ready.NoticesSignature.
func SignNotices(edgeKey ed25519.PrivateKey, serverID, origin string, nonce []byte, notices []Notice) []byte {
	return ed25519.Sign(edgeKey, noticesMessage(serverID, origin, nonce, notices))
}

// VerifyNotices checks that the edge key signed notices for the server
// serverID, which dialed origin and drew nonce for this handshake.
func VerifyNotices(edgeKey ed25519.PublicKey, serverID, origin string, nonce []byte, notices []Notice, signature []byte) error {
	switch {
	case len(edgeKey) != ed25519.PublicKeySize:
		return fmt.Errorf("edgeproto: edge key is %d bytes, want %d", len(edgeKey), ed25519.PublicKeySize)
	case len(nonce) != NonceSize || !ValidServerID(serverID):
		return errors.New("edgeproto: notices are verified for a server id and the nonce it drew")
	case len(signature) == 0:
		return errors.New("edgeproto: the notices are not signed")
	}
	for _, n := range notices {
		if err := n.validate(); err != nil {
			return fmt.Errorf("edgeproto: %w", err)
		}
	}
	if !ed25519.Verify(edgeKey, noticesMessage(serverID, origin, nonce, notices), signature) {
		return errors.New("edgeproto: the notices are not signed by the edge key for this server, address and nonce")
	}
	return nil
}

// FormatRetiring is HeaderRetiring's value: the replacement relay origin,
// then "; date=YYYY-MM-DD" when date is set.
func FormatRetiring(replacement, date string) string {
	if date == "" {
		return replacement
	}
	return replacement + "; date=" + date
}

// ParseRetiring reads a HeaderRetiring value. ok is false unless the
// replacement is a canonical origin and the date, when present, a real
// date.
func ParseRetiring(value string) (replacement, date string, ok bool) {
	replacement, rest, dated := strings.Cut(value, ";")
	replacement = strings.TrimSpace(replacement)
	if dated {
		var found bool
		date, found = strings.CutPrefix(strings.TrimSpace(rest), "date=")
		if !found || !ValidDate(date) {
			return "", "", false
		}
	}
	if !canonical(replacement) {
		return "", "", false
	}
	return replacement, date, true
}
