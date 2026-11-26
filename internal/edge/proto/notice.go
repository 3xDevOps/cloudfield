package edgeproto

import (
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

// HeaderRetiring is on every API answer and WebSocket handshake answer
// of a retiring address pair: FormatRetiring's value, the replacement
// relay origin with an optional date.
const HeaderRetiring = "Aether-Edge-Retiring"

const noticeContext = "aether-edge-notice-v1\x00"

// Notice is one entry of Ready.Notices: the relay origin Origin is in
// State, and Replacement is the relay origin of the same edge to use
// instead. Date, "YYYY-MM-DD" or empty, is text for people. Signature is
// SignNotice's, for one server and one handshake.
type Notice struct {
	State       string `json:"state"`
	Origin      string `json:"origin"`
	Replacement string `json:"replacement"`
	Date        string `json:"date,omitempty"`
	Signature   []byte `json:"signature"`
}

// Notices decodes leniently: an entry that does not decode or validate
// is dropped, and so is everything past MaxRelayOrigins entries, so a
// later edge's notices never refuse the ready they came in.
type Notices []Notice

// UnmarshalJSON keeps the valid entries of data and never fails.
func (n *Notices) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	*n = nil
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	for _, r := range raw {
		var x Notice
		if len(*n) < MaxRelayOrigins && json.Unmarshal(r, &x) == nil && x.validate() == nil {
			*n = append(*n, x)
		}
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
	case len(n.Signature) != ed25519.SignatureSize:
		return fmt.Errorf("notice signature is %d bytes, want %d", len(n.Signature), ed25519.SignatureSize)
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

// noticeMessage is what the edge key signs for one notice:
// "aether-edge-notice-v1\x00" || server id || "\x00" || nonce || state ||
// "\x00" || origin || "\x00" || replacement || "\x00" || date. The nonce
// has a fixed size and no other field can hold a NUL byte.
func noticeMessage(serverID string, nonce []byte, n Notice) []byte {
	msg := make([]byte, 0, len(noticeContext)+len(serverID)+len(nonce)+len(n.State)+len(n.Origin)+len(n.Replacement)+len(n.Date)+4)
	msg = append(msg, noticeContext...)
	msg = append(msg, serverID...)
	msg = append(msg, 0)
	msg = append(msg, nonce...)
	for i, f := range []string{n.State, n.Origin, n.Replacement, n.Date} {
		if i > 0 {
			msg = append(msg, 0)
		}
		msg = append(msg, f...)
	}
	return msg
}

// SignNotice returns n signed with the edge key for the server serverID
// in the handshake whose challenge carried nonce.
func SignNotice(edgeKey ed25519.PrivateKey, serverID string, nonce []byte, n Notice) Notice {
	n.Signature = ed25519.Sign(edgeKey, noticeMessage(serverID, nonce, n))
	return n
}

// VerifyNotice checks n's signature by edgeKey for the server serverID in
// the handshake whose challenge carried nonce.
func VerifyNotice(edgeKey ed25519.PublicKey, serverID string, nonce []byte, n Notice) error {
	switch {
	case len(edgeKey) != ed25519.PublicKeySize:
		return fmt.Errorf("edgeproto: edge key is %d bytes, want %d", len(edgeKey), ed25519.PublicKeySize)
	case len(nonce) != NonceSize || !ValidServerID(serverID):
		return errors.New("edgeproto: a notice is verified for a server id and a handshake nonce")
	}
	if err := n.validate(); err != nil {
		return fmt.Errorf("edgeproto: %w", err)
	}
	if !ed25519.Verify(edgeKey, noticeMessage(serverID, nonce, n), n.Signature) {
		return fmt.Errorf("edgeproto: the notice for %s is not signed by the edge key", n.Origin)
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

// ParseRetiring reads a HeaderRetiring value, or EdgeInfo's Retiring and
// RetiringDate joined by FormatRetiring. ok is false unless the
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
