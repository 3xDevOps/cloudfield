package edge

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// newRetiringHarness serves two pairs and marks the first retiring in
// favour of the second, with date when set.
func newRetiringHarness(t *testing.T, date string) *harness {
	t.Helper()
	return newHarness(t, func(cfg *Config, p *fakeProvider) {
		app := p.app()
		app.ClientID, app.ClientSecret = "test-client-2", "test-secret-2"
		cfg.MorePairs = []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}
		cfg.Retiring = []Retiring{{RelayOrigin: testRelay, Replacement: testRelay2, Date: date}}
	})
}

const (
	bannerDated    = "This address of the edge is retiring on 2027-03-01. Its new address is <code>" + testSignin2 + "</code>: sign in there."
	bannerUndated  = "This address of the edge is retiring. Its new address is <code>" + testSignin2 + "</code>: sign in there."
	joinBannerDate = "This address of the edge is retiring on 2027-03-01. Its new address is <code>" + testRelay2 + "</code>."
)

// TestRetiringHeader checks that every answer on either host of the
// retiring pair names the replacement, and that no answer of the other
// pair, or of an edge with nothing retiring, carries the header.
func TestRetiringHeader(t *testing.T) {
	serverID := testServerID(t)
	paths := []struct{ origin, path string }{
		{testSignin, "/signin"},
		{testSignin, edgeproto.PathServers},
		{testSignin, edgeproto.PathEdgeInfo},
		{testRelay, edgeproto.PathEdgeInfo},
		{testRelay, edgeproto.ConnectPath(serverID)},
		{testRelay, "/join/" + serverID + "/inv-abcdefghjk"},
		{testRelay, "/signin"},
	}
	for _, date := range []string{"", "2027-03-01"} {
		h := newRetiringHarness(t, date)
		want := testRelay2
		if date != "" {
			want += "; date=" + date
		}
		for _, p := range paths {
			if got := retiringHeader(t, h, p.origin, p.path); got != want {
				t.Errorf("date %q: %s%s carries %s %q, want %q", date, p.origin, p.path, edgeproto.HeaderRetiring, got, want)
			}
			other := strings.Replace(strings.Replace(p.origin, "signin.", "signin2.", 1), "relay.", "relay2.", 1)
			if got := retiringHeader(t, h, other, p.path); got != "" {
				t.Errorf("date %q: %s%s of the pair that stays carries %s %q", date, other, p.path, edgeproto.HeaderRetiring, got)
			}
		}
		if got := retiringHeader(t, h, testSignin, "/healthz"); got != "" {
			t.Errorf("/healthz carries %s %q", edgeproto.HeaderRetiring, got)
		}
	}
	h := newPairsHarness(t)
	for _, p := range paths {
		if got := retiringHeader(t, h, p.origin, p.path); got != "" {
			t.Errorf("nothing retiring: %s%s carries %s %q", p.origin, p.path, edgeproto.HeaderRetiring, got)
		}
	}
}

func retiringHeader(t *testing.T, h *harness, origin, path string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, origin+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(edgeproto.HeaderVersion, "1")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()               //nolint:errcheck // test
	_, _ = io.Copy(io.Discard, resp.Body) // a test answer
	if n := len(resp.Header.Values(edgeproto.HeaderRetiring)); n > 1 {
		t.Errorf("%s%s carries %s %d times", origin, path, edgeproto.HeaderRetiring, n)
	}
	return resp.Header.Get(edgeproto.HeaderRetiring)
}

func TestRetiringEdgeInfo(t *testing.T) {
	for _, date := range []string{"", "2027-03-01"} {
		h := newRetiringHarness(t, date)
		var info edgeproto.EdgeInfo
		if status, msg := h.apiCallOn(t, testRelay, http.MethodGet, edgeproto.PathEdgeInfo, "", nil, &info); status != http.StatusOK {
			t.Fatalf("edge info: %d %s", status, msg)
		}
		if info.Retiring != testRelay2 || info.RetiringDate != date || info.SigninOrigin != testSignin {
			t.Errorf("date %q: edge info of the retiring pair %+v", date, info)
		}
		raw := edgeInfoJSON(t, h, testRelay2)
		if _, ok := raw["retiring"]; ok {
			t.Errorf("edge info of the pair that stays carries retiring: %v", raw)
		}
		if _, ok := raw["retiring_date"]; ok {
			t.Errorf("edge info of the pair that stays carries retiring_date: %v", raw)
		}
	}
	raw := edgeInfoJSON(t, newRetiringHarness(t, ""), testRelay)
	if _, ok := raw["retiring_date"]; ok {
		t.Errorf("edge info without a date carries retiring_date: %v", raw)
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		return out
	}
	if got := edgeInfoJSON(t, newPairsHarness(t), testRelay); len(got) != 5 {
		t.Errorf("edge info with nothing retiring has the members %v, want the five it had", keys(got))
	}
}

func edgeInfoJSON(t *testing.T, h *harness, origin string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, origin+edgeproto.PathEdgeInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(edgeproto.HeaderVersion, "1")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestRetiringBanner checks the banner on the pages of the retiring pair:
// the sign-in host's, signed out and in, and the join page on the relay
// host; and that the other pair's pages carry none.
func TestRetiringBanner(t *testing.T) {
	serverID := testServerID(t)
	join := "/join/" + serverID + "/inv-abcdefghjk"
	for _, tt := range []struct{ date, signin, join string }{
		{"2027-03-01", bannerDated, joinBannerDate},
		{"", bannerUndated, "This address of the edge is retiring. Its new address is <code>" + testRelay2 + "</code>."},
	} {
		h := newRetiringHarness(t, tt.date)
		h.setGitHubUser(1001, "octo", "octo@example.test", true)
		b := h.browser(t)
		for _, path := range []string{"/signin", "/signin/github/callback?code=x&state=y"} {
			if _, page := b.get(t, path); strings.Count(page, tt.signin) != 1 {
				t.Errorf("date %q: %s shows no banner, or more than one:\n%s", tt.date, path, page)
			}
		}
		b.signIn(t)
		for _, path := range []string{"/servers", "/devices", "/device", "/account"} {
			if _, page := b.get(t, path); strings.Count(page, tt.signin) != 1 {
				t.Errorf("date %q: %s shows no banner, or more than one:\n%s", tt.date, path, page)
			}
		}
		relay := h.browser(t)
		relay.origin = testRelay
		if _, page := relay.get(t, join); strings.Count(page, tt.join) != 1 {
			t.Errorf("date %q: the join page shows no banner, or more than one:\n%s", tt.date, page)
		}
		for _, origin := range []string{testSignin2, testRelay2} {
			other := h.browser(t)
			other.origin = origin
			path := "/signin"
			if origin == testRelay2 {
				path = join
			}
			if _, page := other.get(t, path); strings.Contains(page, "retiring") {
				t.Errorf("date %q: %s%s of the pair that stays shows a banner:\n%s", tt.date, origin, path, page)
			}
		}
	}
}

func TestRetiringNotices(t *testing.T) {
	h := newRetiringHarness(t, "2027-03-01")
	want := []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: testRelay, Replacement: testRelay2, Date: "2027-03-01"}}
	if got := h.svc.RelayConfig(0).Notices; !reflect.DeepEqual(got, want) {
		t.Errorf("relay notices %+v, want %+v", got, want)
	}
	if got := newPairsHarness(t).svc.RelayConfig(0).Notices; got != nil {
		t.Errorf("relay notices with nothing retiring %+v, want none", got)
	}
}

func TestRetiringNeedsTwoServedPairs(t *testing.T) {
	app := &OAuthApp{ClientID: "id", ClientSecret: "secret"}
	for _, r := range []Retiring{
		{RelayOrigin: "https://relay3.example.test", Replacement: testRelay2},
		{RelayOrigin: testRelay, Replacement: "https://relay3.example.test"},
		{RelayOrigin: testRelay, Replacement: testRelay},
		{RelayOrigin: testSignin, Replacement: testRelay2},
	} {
		_, err := New(Config{DataDir: t.TempDir(), SigninOrigin: testSignin, RelayOrigin: testRelay, GitHub: app,
			MorePairs: []Pair{{SigninOrigin: testSignin2, RelayOrigin: testRelay2, GitHub: app}}, Retiring: []Retiring{r}})
		want := "edge: retiring " + r.RelayOrigin + " in favour of " + r.Replacement + ": both must be relay origins of two pairs this edge serves"
		if err == nil || err.Error() != want {
			t.Errorf("New retiring %+v = %v, want %q", r, err, want)
		}
	}
}

// The banner is text only: the address is escaped and is no link.
func TestRetiringBannerIsText(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	const hostile = `https://x.example"><a href="https://evil.example/?code=1">sign in</a><script>alert(1)</script>`
	st := galleryState{name: "hostile", page: "signin", status: 200,
		view: view{Title: "Sign in", Data: signinData{GitHub: "/signin/github"}, Retiring: &banner{Address: hostile, Date: `<b>2027</b>`, SignIn: true}}}
	page := renderState(t, &Service{pages: pages}, st).Body.String()
	i := strings.Index(page, "This address of the edge is retiring")
	if i < 0 {
		t.Fatalf("no banner:\n%s", page)
	}
	line := page[i:]
	line = line[:strings.Index(line, "</p>")]
	for _, markup := range []string{"<a", "<script", "<b>"} {
		if strings.Contains(line, markup) {
			t.Errorf("banner %q carries %s", line, markup)
		}
	}
	if !strings.Contains(line, html.EscapeString(hostile)) || !strings.Contains(line, "&lt;b&gt;2027&lt;/b&gt;") {
		t.Errorf("banner %q does not show the address and date as escaped text", line)
	}
}

// TestPagesWithoutBannerUnchanged renders every page state with nothing
// retiring, once with the layout and once with the layout without the
// banner's block: the bytes are the same.
func TestPagesWithoutBannerUnchanged(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	layout, err := templateFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)\{\{with \.Retiring\}\}.*?\{\{end -\}\}\n`)
	if !block.Match(layout) {
		t.Fatal("no banner block in templates/layout.html")
	}
	without, err := pages.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = without.Parse(string(block.ReplaceAll(layout, nil))); err != nil {
		t.Fatal(err)
	}
	for _, st := range galleryStates(t) {
		if st.view.Retiring != nil {
			continue
		}
		got, want := renderState(t, &Service{pages: pages}, st), renderState(t, &Service{pages: without}, st)
		if got.Body.String() != want.Body.String() {
			t.Errorf("%s with nothing retiring differs from the page without the banner:\n%s\nwant\n%s", st.name, got.Body, want.Body)
		}
	}
}
