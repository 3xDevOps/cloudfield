package edgeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

const noticeServer = "7ozstx3hvprfzhna3cakjwhdnw"

func testNotice() Notice {
	return Notice{State: NoticeRetiring, Origin: "https://edge.example", Replacement: "https://edge2.example", Date: "2027-03-01"}
}

func TestNoticeSignature(t *testing.T) {
	key, nonce := edgeKey(1), bytes.Repeat([]byte{1}, NonceSize)
	pub := edgeKey(1).Public().(ed25519.PublicKey)
	signed := SignNotice(key, noticeServer, nonce, testNotice())
	if err := VerifyNotice(pub, noticeServer, nonce, signed); err != nil {
		t.Fatalf("VerifyNotice of a signed notice: %v", err)
	}
	undated := SignNotice(key, noticeServer, nonce, Notice{State: NoticeRetired, Origin: "https://edge.example", Replacement: "https://edge2.example"})
	if err := VerifyNotice(pub, noticeServer, nonce, undated); err != nil {
		t.Fatalf("VerifyNotice of a signed notice without a date: %v", err)
	}

	otherNonce := bytes.Repeat([]byte{2}, NonceSize)
	refused := map[string]func() error{
		"state": func() error { n := signed; n.State = NoticeRetired; return VerifyNotice(pub, noticeServer, nonce, n) },
		"origin": func() error {
			n := signed
			n.Origin = "https://edge3.example"
			return VerifyNotice(pub, noticeServer, nonce, n)
		},
		"replacement": func() error {
			n := signed
			n.Replacement = "https://edge3.example"
			return VerifyNotice(pub, noticeServer, nonce, n)
		},
		"date":         func() error { n := signed; n.Date = "2027-03-02"; return VerifyNotice(pub, noticeServer, nonce, n) },
		"date removed": func() error { n := signed; n.Date = ""; return VerifyNotice(pub, noticeServer, nonce, n) },
		"origin and replacement swapped": func() error {
			n := signed
			n.Origin, n.Replacement = n.Replacement, n.Origin
			return VerifyNotice(pub, noticeServer, nonce, n)
		},
		"wrong key": func() error {
			return VerifyNotice(edgeKey(2).Public().(ed25519.PublicKey), noticeServer, nonce, signed)
		},
		"wrong server id": func() error { return VerifyNotice(pub, "aaaaaaaaaaaaaaaaaaaaaaaaaa", nonce, signed) },
		"wrong nonce":     func() error { return VerifyNotice(pub, noticeServer, otherNonce, signed) },
		"short nonce":     func() error { return VerifyNotice(pub, noticeServer, nonce[:16], signed) },
		"no signature":    func() error { n := signed; n.Signature = nil; return VerifyNotice(pub, noticeServer, nonce, n) },
		"short key":       func() error { return VerifyNotice(pub[:16], noticeServer, nonce, signed) },
	}
	for name, verify := range refused {
		if err := verify(); err == nil {
			t.Errorf("%s: VerifyNotice accepted a notice that does not match its signature", name)
		}
	}
}

// TestNoticeFieldsCannotShift checks that moving bytes from one field to
// the next changes what is signed.
func TestNoticeFieldsCannotShift(t *testing.T) {
	nonce := bytes.Repeat([]byte{1}, NonceSize)
	a := noticeMessage(noticeServer, nonce, Notice{State: "retiring", Origin: "https://a.example", Replacement: "https://b.example", Date: ""})
	b := noticeMessage(noticeServer, nonce, Notice{State: "retiring", Origin: "https://a.example", Replacement: "", Date: "https://b.example"})
	if bytes.Equal(a, b) {
		t.Fatal("two different notices sign the same message")
	}
}

func TestReadyNoticesRoundTrip(t *testing.T) {
	nonce := bytes.Repeat([]byte{1}, NonceSize)
	n := SignNotice(edgeKey(1), noticeServer, nonce, testNotice())
	r := Ready{ServerID: noticeServer, State: StateClaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey), Notices: Notices{n}}
	data, err := EncodeControl(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"notices":[{"state":"retiring","origin":"https://edge.example","replacement":"https://edge2.example","date":"2027-03-01","signature":"`) {
		t.Fatalf("ready encodes notices as %s", data)
	}
	m, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.(Ready).Notices; len(got) != 1 || VerifyNotice(r.EdgeKey, noticeServer, nonce, got[0]) != nil {
		t.Fatalf("decoded notices %+v do not verify", got)
	}

	bad := r
	bad.Notices = Notices{{State: "moved", Origin: n.Origin, Replacement: n.Replacement, Signature: n.Signature}}
	if _, err := EncodeControl(bad); err == nil {
		t.Fatal("EncodeControl sent a notice with an unknown state")
	}
}

// TestReadyWithoutNoticesUnchanged checks that a ready without notices
// encodes as builds before notices sent it.
func TestReadyWithoutNoticesUnchanged(t *testing.T) {
	want, err := os.ReadFile("testdata/v1/ready.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := EncodeControl(Ready{ServerID: noticeServer, State: StateUnclaimed, EdgeKey: unb64(t, "zswVB9wd3XKVlRwpCIjwla25BE0bc9aW5t8GXWg71Pw=")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimSpace(want)) {
		t.Fatalf("ready without notices encodes as\n%s\nwant\n%s", got, want)
	}
}

func TestReadyKeepsMaxRelayOriginsNotices(t *testing.T) {
	nonce := bytes.Repeat([]byte{1}, NonceSize)
	var sent []Notice
	for i := range MaxRelayOrigins + 3 {
		n := testNotice()
		n.Origin = fmt.Sprintf("https://old%d.example", i)
		sent = append(sent, SignNotice(edgeKey(1), noticeServer, nonce, n))
	}
	data, err := json.Marshal(map[string]any{"type": "ready", "server_id": noticeServer, "state": StateClaimed,
		"edge_key": edgeKey(1).Public(), "notices": sent})
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecodeControl(data)
	if err != nil {
		t.Fatalf("ready with too many notices refused: %v", err)
	}
	got := m.(Ready).Notices
	if len(got) != MaxRelayOrigins || got[0].Origin != sent[0].Origin || got[MaxRelayOrigins-1].Origin != sent[MaxRelayOrigins-1].Origin {
		t.Fatalf("kept %d notices, want the first %d", len(got), MaxRelayOrigins)
	}
}

func TestParseRetiring(t *testing.T) {
	for _, c := range []struct{ value, replacement, date string }{
		{"https://edge2.example", "https://edge2.example", ""},
		{"https://edge2.example; date=2027-03-01", "https://edge2.example", "2027-03-01"},
		{"https://edge2.example;date=2027-03-01", "https://edge2.example", "2027-03-01"},
		{"  https://edge2.example:8443 ", "https://edge2.example:8443", ""},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", ""},
	} {
		r, d, ok := ParseRetiring(c.value)
		if !ok || r != c.replacement || d != c.date {
			t.Errorf("ParseRetiring(%q) = %q, %q, %v; want %q, %q", c.value, r, d, ok, c.replacement, c.date)
		}
	}
	for _, v := range []string{
		"", "edge2.example", "https://EDGE2.example", "https://edge2.example/", "https://edge2.example/path",
		"http://edge2.example", "https://edge2.example; date=", "https://edge2.example; date=2027-02-30",
		"https://edge2.example; date=2027-3-1", "https://edge2.example; on=2027-03-01", "https://edge2.example; date=2027-03-01; x=y",
		"https://edge2.example\x1b[2J", "https://<new-relay-host>",
	} {
		if r, d, ok := ParseRetiring(v); ok {
			t.Errorf("ParseRetiring(%q) = %q, %q, true; want refused", v, r, d)
		}
	}
	if got := FormatRetiring("https://edge2.example", "2027-03-01"); got != "https://edge2.example; date=2027-03-01" {
		t.Errorf("FormatRetiring with a date = %q", got)
	}
	if got := FormatRetiring("https://edge2.example", ""); got != "https://edge2.example" {
		t.Errorf("FormatRetiring without a date = %q", got)
	}
}
