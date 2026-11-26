package edgeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	noticeServer = "7ozstx3hvprfzhna3cakjwhdnw"
	noticeOrigin = "https://edge.example"
)

func testNotices() []Notice {
	return []Notice{
		{State: NoticeRetiring, Origin: "https://edge.example", Replacement: "https://edge2.example", Date: "2027-03-01"},
		{State: NoticeRetired, Origin: "https://edge0.example", Replacement: "https://edge2.example"},
	}
}

func TestNoticesSignature(t *testing.T) {
	key, nonce := edgeKey(1), bytes.Repeat([]byte{2}, NonceSize)
	pub := key.Public().(ed25519.PublicKey)
	list := testNotices()
	sig := SignNotices(key, noticeServer, noticeOrigin, nonce, list)
	if err := VerifyNotices(pub, noticeServer, noticeOrigin, nonce, list, sig); err != nil {
		t.Fatalf("VerifyNotices of a signed list: %v", err)
	}
	empty := SignNotices(key, noticeServer, noticeOrigin, nonce, nil)
	if err := VerifyNotices(pub, noticeServer, noticeOrigin, nonce, nil, empty); err != nil {
		t.Fatalf("VerifyNotices of a signed empty list: %v", err)
	}

	edited := func(edit func(l []Notice) []Notice) []Notice {
		return edit(append([]Notice(nil), list...))
	}
	refused := map[string]error{
		"state": VerifyNotices(pub, noticeServer, noticeOrigin, nonce, edited(func(l []Notice) []Notice { l[0].State = NoticeRetired; return l }), sig),
		"origin": VerifyNotices(pub, noticeServer, noticeOrigin, nonce,
			edited(func(l []Notice) []Notice { l[0].Origin = "https://edge3.example"; return l }), sig),
		"replacement": VerifyNotices(pub, noticeServer, noticeOrigin, nonce,
			edited(func(l []Notice) []Notice { l[1].Replacement = "https://edge3.example"; return l }), sig),
		"date":            VerifyNotices(pub, noticeServer, noticeOrigin, nonce, edited(func(l []Notice) []Notice { l[0].Date = "2027-03-02"; return l }), sig),
		"date removed":    VerifyNotices(pub, noticeServer, noticeOrigin, nonce, edited(func(l []Notice) []Notice { l[0].Date = ""; return l }), sig),
		"entry dropped":   VerifyNotices(pub, noticeServer, noticeOrigin, nonce, list[:1], sig),
		"all dropped":     VerifyNotices(pub, noticeServer, noticeOrigin, nonce, nil, sig),
		"entries swapped": VerifyNotices(pub, noticeServer, noticeOrigin, nonce, []Notice{list[1], list[0]}, sig),
		"entry added": VerifyNotices(pub, noticeServer, noticeOrigin, nonce,
			append(edited(func(l []Notice) []Notice { return l }), Notice{State: NoticeRetiring, Origin: "https://edge3.example", Replacement: "https://edge2.example"}), sig),
		"empty list for a full one": VerifyNotices(pub, noticeServer, noticeOrigin, nonce, list, empty),
		"wrong key":                 VerifyNotices(edgeKey(2).Public().(ed25519.PublicKey), noticeServer, noticeOrigin, nonce, list, sig),
		"wrong server id":           VerifyNotices(pub, "aaaaaaaaaaaaaaaaaaaaaaaaaa", noticeOrigin, nonce, list, sig),
		"wrong origin":              VerifyNotices(pub, noticeServer, "https://edge2.example", nonce, list, sig),
		"wrong nonce":               VerifyNotices(pub, noticeServer, noticeOrigin, bytes.Repeat([]byte{3}, NonceSize), list, sig),
		"short nonce":               VerifyNotices(pub, noticeServer, noticeOrigin, nonce[:16], list, sig),
		"no signature":              VerifyNotices(pub, noticeServer, noticeOrigin, nonce, list, nil),
		"short signature":           VerifyNotices(pub, noticeServer, noticeOrigin, nonce, list, sig[:32]),
		"short key":                 VerifyNotices(pub[:16], noticeServer, noticeOrigin, nonce, list, sig),
	}
	for name, err := range refused {
		if err == nil {
			t.Errorf("%s: VerifyNotices accepted notices that do not match their signature", name)
		}
	}
}

// TestNoticesFieldsCannotShift checks that moving bytes between fields,
// or between entries, changes what is signed.
func TestNoticesFieldsCannotShift(t *testing.T) {
	nonce := bytes.Repeat([]byte{1}, NonceSize)
	a := noticesMessage(noticeServer, noticeOrigin, nonce, []Notice{{State: "retiring", Origin: "https://a.example", Replacement: "https://b.example"}})
	b := noticesMessage(noticeServer, noticeOrigin, nonce, []Notice{{State: "retiring", Origin: "https://a.example", Date: "https://b.example"}})
	c := noticesMessage(noticeServer, noticeOrigin+"x", nonce[:NonceSize-1], nil)
	if bytes.Equal(a, b) || bytes.HasPrefix(a, c) {
		t.Fatal("two different notice lists sign the same message")
	}
}

// TestNoticesContextIsItsOwn checks that a notice signature can never
// verify as a grant, a connector assertion or an enrollment: every
// message the edge key or a host key signs starts with its own context,
// and no context is a prefix of another.
func TestNoticesContextIsItsOwn(t *testing.T) {
	contexts := []string{noticesContext, grantContext, assertionContext, enrollContext}
	for i, a := range contexts {
		for j, b := range contexts {
			if i != j && strings.HasPrefix(b, a) {
				t.Errorf("signing context %q is a prefix of %q", a, b)
			}
		}
	}
	if msg := noticesMessage(noticeServer, noticeOrigin, make([]byte, NonceSize), nil); !bytes.HasPrefix(msg, []byte(noticesContext)) {
		t.Errorf("notices message %q does not start with its context", msg)
	}
}

func TestReadyNoticesRoundTrip(t *testing.T) {
	nonce := bytes.Repeat([]byte{2}, NonceSize)
	list := testNotices()
	r := Ready{ServerID: noticeServer, State: StateClaimed, EdgeKey: edgeKey(1).Public().(ed25519.PublicKey), Notices: list,
		NoticesSignature: SignNotices(edgeKey(1), noticeServer, noticeOrigin, nonce, list)}
	data, err := EncodeControl(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"notices":[{"state":"retiring","origin":"https://edge.example","replacement":"https://edge2.example","date":"2027-03-01"},`+
		`{"state":"retired","origin":"https://edge0.example","replacement":"https://edge2.example"}],"notices_signature":"`) {
		t.Fatalf("ready encodes notices as %s", data)
	}
	m, err := DecodeControl(data)
	if err != nil {
		t.Fatal(err)
	}
	got := m.(Ready)
	if err := VerifyNotices(r.EdgeKey, noticeServer, noticeOrigin, nonce, got.Notices, got.NoticesSignature); err != nil {
		t.Fatalf("decoded notices do not verify: %v", err)
	}

	bad := r
	bad.Notices = Notices{{State: "moved", Origin: list[0].Origin, Replacement: list[0].Replacement}}
	if _, err := EncodeControl(bad); err == nil {
		t.Fatal("EncodeControl sent a notice with an unknown state")
	}
}

// TestNoticeFixturesVerify checks the signed fixture, and the fixture
// whose notices hold entries this build cannot read: that ready is
// accepted, and the list it keeps no longer matches the signature.
func TestNoticeFixturesVerify(t *testing.T) {
	for name, valid := range map[string]bool{"ready_notices": true, "ready_bad_notices": false, "ready_notices_not_a_list": false} {
		data, err := os.ReadFile("testdata/v1/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		m, err := DecodeControl(bytes.TrimSpace(data))
		if err != nil {
			t.Fatalf("%s refused: %v", name, err)
		}
		r := m.(Ready)
		err = VerifyNotices(r.EdgeKey, noticeServer, noticeOrigin, bytes.Repeat([]byte{2}, NonceSize), r.Notices, r.NoticesSignature)
		if (err == nil) != valid {
			t.Errorf("%s: VerifyNotices = %v, want valid %v", name, err, valid)
		}
	}
}

// TestReadyWithoutNoticesUnchanged checks that a ready without notices
// encodes as builds before notices sent it, and a hello without a notice
// nonce as before.
func TestReadyWithoutNoticesUnchanged(t *testing.T) {
	for name, m := range map[string]Message{
		"ready": Ready{ServerID: noticeServer, State: StateUnclaimed, EdgeKey: unb64(t, "zswVB9wd3XKVlRwpCIjwla25BE0bc9aW5t8GXWg71Pw=")},
		"hello": Hello{Version: 1, HostKey: unb64(t, "AAAAC3NzaC1lZDI1NTE5AAAAIM7MFQfcHd1ylZUcKQiI8JWtuQRNG3PWlubfBl1oO9T8"),
			Signature:    unb64(t, "AAAAC3NzaC1lZDI1NTE5AAAAQAr+l1EewoxI2CWsih4ooJYBCv6geC+TUnQv7ztyto9FL/JTiIUc6J9ik4XCjDkVueYu9iV2IrOKJfC6n/Y4tAc="),
			AgentVersion: "v0.0.0-test", Name: "devbox"},
	} {
		want, err := os.ReadFile("testdata/v1/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		got, err := EncodeControl(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, bytes.TrimSpace(want)) {
			t.Errorf("%s without the new fields encodes as\n%s\nwant\n%s", name, got, want)
		}
	}
}

// TestReadyNoticesCap checks that a list of MaxRelayOrigins notices is
// kept, and a longer one dropped whole without refusing the ready.
func TestReadyNoticesCap(t *testing.T) {
	for _, n := range []int{MaxRelayOrigins, MaxRelayOrigins + 1} {
		var sent []Notice
		for i := range n {
			sent = append(sent, Notice{State: NoticeRetiring, Origin: fmt.Sprintf("https://old%d.example", i), Replacement: "https://edge2.example"})
		}
		data, err := json.Marshal(map[string]any{"type": "ready", "server_id": noticeServer, "state": StateClaimed,
			"edge_key": edgeKey(1).Public(), "notices": sent, "notices_signature": bytes.Repeat([]byte{1}, 200)})
		if err != nil {
			t.Fatal(err)
		}
		m, err := DecodeControl(data)
		if err != nil {
			t.Fatalf("ready with %d notices or a long signature refused: %v", n, err)
		}
		got := m.(Ready).Notices
		if want := sent; n > MaxRelayOrigins {
			if got != nil {
				t.Errorf("%d notices: kept %d, want none", n, len(got))
			}
		} else if !slices.Equal(got, want) {
			t.Errorf("%d notices: kept %d, want all", n, len(got))
		}
	}
}

// TestEdgeInfoIgnoresANoticeOfAnotherType decodes edge metadata whose
// retiring fields are not strings: the rest decodes, the notice is gone.
func TestEdgeInfoIgnoresANoticeOfAnotherType(t *testing.T) {
	key := edgeKey(1).Public().(ed25519.PublicKey)
	for _, extra := range []string{
		`"retiring":5`, `"retiring":"https://edge2.example","retiring_date":20270301`, `"retiring":{"origin":"https://edge2.example"}`,
	} {
		base, err := json.Marshal(EdgeInfo{SigninOrigin: "https://auth.example", Key: key, Fingerprint: EdgeKeyFingerprint(key), Version: 6, MinVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		data := strings.TrimSuffix(string(base), "}") + "," + extra + "}"
		var info EdgeInfo
		if err := json.Unmarshal([]byte(data), &info); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		if info.Retiring != "" || info.RetiringDate != "" || info.SigninOrigin != "https://auth.example" || info.Validate() != nil {
			t.Errorf("%s decodes as %+v", data, info)
		}
	}
	var info EdgeInfo
	if err := json.Unmarshal([]byte(`{"signin_origin":"https://auth.example","retiring":"https://edge2.example","retiring_date":"2027-03-01"}`), &info); err != nil ||
		info.Retiring != "https://edge2.example" || info.RetiringDate != "2027-03-01" {
		t.Errorf("string fields decode as %+v, %v", info, err)
	}
}

func TestHelloNoticeNonceNeverRefuses(t *testing.T) {
	data, err := os.ReadFile("testdata/v1/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	odd := strings.Replace(string(bytes.TrimSpace(data)), `"access_policy":""`, `"access_policy":"","notice_nonce":"AQID"`, 1)
	m, err := DecodeControl([]byte(odd))
	if err != nil {
		t.Fatalf("hello with a short notice nonce refused: %v", err)
	}
	if n := m.(Hello).NoticeNonce; len(n) != 3 {
		t.Fatalf("notice nonce %x", n)
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
