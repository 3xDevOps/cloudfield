package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme/autocert"

	edge "github.com/3xDevOps/cloudfield/internal/edge/service"
	"github.com/3xDevOps/cloudfield/internal/envname"
)

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// origins are the two origins of a public edge; devOrigins of a local
// one, whose loopback hosts differ by name.
var (
	origins    = []string{"--signin-origin", "https://auth.example.test", "--relay-origin", "https://edge.example.test"}
	devOrigins = []string{"--signin-origin", "http://localhost:8080", "--relay-origin", "http://127.0.0.1:8080"}
)

func TestPlainHTTPIsLoopbackOnly(t *testing.T) {
	for _, mode := range []struct {
		flag    string
		origins []string
	}{{"--dev-listen", devOrigins}, {"--proxy-listen", origins}} {
		for _, addr := range []string{"0.0.0.0:8080", ":8080", "localhost:8080", "192.0.2.1:8080", "[::]:8080"} {
			_, err := parseOptions(append([]string{mode.flag, addr}, mode.origins...), envOf(nil))
			if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Errorf("%s %s: %v, want a loopback refusal", mode.flag, addr, err)
			}
		}
		for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080"} {
			if _, err := parseOptions(append([]string{mode.flag, addr}, mode.origins...), envOf(nil)); err != nil {
				t.Errorf("%s %s: %v", mode.flag, addr, err)
			}
		}
	}
	if _, err := parseOptions(append([]string{"--metrics-listen", "0.0.0.0:9464"}, origins...), envOf(nil)); err == nil {
		t.Error("metrics on a public address accepted")
	}
	for _, args := range [][]string{
		devOrigins,
		append([]string{"--proxy-listen", "127.0.0.1:8443"}, devOrigins...),
	} {
		if _, err := parseOptions(args, envOf(nil)); err == nil || !strings.Contains(err.Error(), "must be https://host[:port]") {
			t.Errorf("%v: %v, want plain HTTP origins refused outside development mode", args, err)
		}
	}
	if _, err := parseOptions(append([]string{"--dev-listen", "127.0.0.1:8080"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "--dev-listen serves plain HTTP, so --signin-origin must be http://") {
		t.Errorf("https origins in development mode: %v", err)
	}
	if _, err := parseOptions(append([]string{"--dev-listen", "127.0.0.1:8080", "--proxy-listen", "127.0.0.1:8443"}, devOrigins...), envOf(nil)); err == nil {
		t.Error("--dev-listen and --proxy-listen together accepted")
	}
}

func TestOriginsAreTwoHosts(t *testing.T) {
	for _, args := range [][]string{
		{"--signin-origin", "https://edge.example.test", "--relay-origin", "https://edge.example.test"},
		{"--signin-origin", "https://edge.example.test:8443", "--relay-origin", "https://EDGE.example.test"},
	} {
		if _, err := parseOptions(args, envOf(nil)); err == nil || !strings.Contains(err.Error(), "must name different hosts") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if _, err := parseOptions([]string{"--relay-origin", "https://edge.example.test"}, envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), `--signin-origin must be https://host[:port], not ""`) {
		t.Errorf("no sign-in origin: %v", err)
	}
	o, err := parseOptions(nil, envOf(map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_PROXY_LISTEN": "127.0.0.1:8443",
	}))
	if err != nil || o.signinOrigin != "https://auth.example.test" || o.relayOrigin != "https://edge.example.test" || o.proxyListen != "127.0.0.1:8443" {
		t.Fatalf("options from the environment: %+v %v", o, err)
	}
}

func TestClientSecrets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(file, []byte("fake-secret-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := append(slices.Clone(origins), "--github-client-id", "fake-id")

	o, err := parseOptions(append(base, "--github-client-secret-file", file), envOf(nil))
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret-from-file" {
		t.Fatalf("secret from file: %+v %v", o.github, err)
	}
	o, err = parseOptions(base, envOf(map[string]string{"AETHER_EDGE_GITHUB_CLIENT_SECRET": "fake-secret-from-env"}))
	if err != nil || o.github.ClientSecret != "fake-secret-from-env" {
		t.Fatalf("secret from the environment: %+v %v", o.github, err)
	}
	if _, err := parseOptions(base, envOf(nil)); err == nil {
		t.Error("client id without a secret accepted")
	}
	if _, err := parseOptions(append(base, "--github-client-secret-file", file),
		envOf(map[string]string{"AETHER_EDGE_GITHUB_CLIENT_SECRET": "fake"})); err == nil {
		t.Error("secret from both the environment and a file accepted")
	}
	// A secret is never a flag: its value would show in the process list.
	if _, err := parseOptions(append(base, "--github-client-secret", "fake"), envOf(nil)); err == nil {
		t.Error("--github-client-secret accepted")
	}
}

func TestOriginMustNameAHost(t *testing.T) {
	// The placeholders the environment file ships with.
	_, err := parseOptions([]string{"--signin-origin", "https://<signin-host>", "--relay-origin", "https://<relay-host>"}, envOf(nil))
	if err == nil || !strings.Contains(err.Error(), `--signin-origin: edgeproto: edge url "https://<signin-host>": host "<signin-host>" is not a DNS name or an IP address`) {
		t.Fatalf("placeholder origin: %v", err)
	}
}

// TestSecretUnderTheUnit starts as packaging/systemd/aether-edge.service
// does: the secret variable names a credential, which SetCredential=
// leaves a single newline when /etc/aether-edge/github-client-secret is
// missing.
func TestSecretUnderTheUnit(t *testing.T) {
	github := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(github, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN":             "https://auth.example.test",
		"AETHER_EDGE_RELAY_ORIGIN":              "https://edge.example.test",
		"AETHER_EDGE_GITHUB_CLIENT_ID":          "fake-id",
		"AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": github,
	}
	o, err := parseOptions(nil, envOf(env))
	if err != nil || o.github == nil || o.github.ClientSecret != "fake-secret" {
		t.Fatalf("secret from the credential: %+v, %v", o.github, err)
	}

	if err := os.WriteFile(github, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions(nil, envOf(env)); err == nil || !strings.Contains(err.Error(), github+", named by --github-client-secret-file, holds no secret") {
		t.Fatalf("client id without a secret: %v", err)
	}
}

func TestTrustedProxies(t *testing.T) {
	proxy := func(listen, list string) (options, error) {
		return parseOptions(append([]string{"--proxy-listen", listen, "--trusted-proxies", list}, origins...), envOf(nil))
	}
	o, err := proxy("0.0.0.0:8443", "172.18.0.0/16, fd00:18::/64,::ffff:10.0.0.5/128,192.0.2.7/24")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"172.18.0.0/16", "fd00:18::/64", "10.0.0.5/32", "192.0.2.0/24"}
	if got := fmt.Sprint(o.trustedProxies); got != fmt.Sprint(want) {
		t.Errorf("trusted proxies %s, want %v", got, want)
	}
	for _, listen := range []string{":8443", "[::]:8443", "172.18.0.5:8443"} {
		if _, err = proxy(listen, "172.18.0.0/16"); err != nil {
			t.Errorf("--proxy-listen %s with --trusted-proxies: %v", listen, err)
		}
	}
	for list, want := range map[string]string{
		"0.0.0.0/0":                  `"0.0.0.0/0" trusts every address`,
		"172.18.0.0/16,::/0":         `"::/0" trusts every address`,
		"::ffff:0.0.0.0/96":          `"::ffff:0.0.0.0/96" trusts every address`,
		"172.18.0.5":                 `"172.18.0.5" is not a network such as 172.18.0.0/16`,
		"172.18.0.0/16,":             `"" is not a network`,
		"172.18.0.0/33":              `"172.18.0.0/33" is not a network`,
		"proxy.internal/32":          `"proxy.internal/32" is not a network`,
		"fe80::1%eth0/64":            `"fe80::1%eth0/64" is not a network`,
		"172.18.0.0/16;10.0.0.0/8":   `"172.18.0.0/16;10.0.0.0/8" is not a network`,
		"172.18.0.0/16 10.0.0.0/8/8": `is not a network`,
	} {
		if _, err = proxy("0.0.0.0:8443", list); err == nil || !strings.Contains(err.Error(), "--trusted-proxies: ") || !strings.Contains(err.Error(), want) {
			t.Errorf("--trusted-proxies %q: %v, want %q", list, err, want)
		}
	}
	if _, err = parseOptions(append([]string{"--trusted-proxies", "172.18.0.0/16"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "set --proxy-listen too") {
		t.Errorf("--trusted-proxies without --proxy-listen: %v", err)
	}
	// Without the option, --proxy-listen stays loopback only, and says how
	// to trust a proxy elsewhere.
	if _, err = parseOptions(append([]string{"--proxy-listen", "0.0.0.0:8443"}, origins...), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "must be a loopback address") || !strings.Contains(err.Error(), "--trusted-proxies") {
		t.Errorf("non-loopback --proxy-listen without --trusted-proxies: %v", err)
	}
	o, err = parseOptions(nil, envOf(map[string]string{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_PROXY_LISTEN": ":8443", "AETHER_EDGE_TRUSTED_PROXIES": "10.0.0.0/8",
	}))
	if err != nil || fmt.Sprint(o.trustedProxies) != "[10.0.0.0/8]" {
		t.Errorf("--trusted-proxies from the environment: %v %v", o.trustedProxies, err)
	}
}

func TestSecretFileErrorsSayWhatToDo(t *testing.T) {
	dir := t.TempDir()
	base := append(slices.Clone(origins), "--github-client-id", "fake-id")
	_, err := parseOptions(append(base, "--github-client-secret-file", filepath.Join(dir, "missing")), envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "--github-client-secret-file: open "+filepath.Join(dir, "missing")+": no such file or directory; put the secret there") {
		t.Errorf("missing secret file: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of any mode")
	}
	unreadable := filepath.Join(dir, "unreadable")
	if err = os.WriteFile(unreadable, []byte("fake-secret\n"), 0); err != nil {
		t.Fatal(err)
	}
	_, err = parseOptions(append(base, "--github-client-secret-file", unreadable), envOf(nil))
	if err == nil || !strings.Contains(err.Error(), "permission denied; make it readable by uid") {
		t.Errorf("unreadable secret file: %v", err)
	}
}

// TestEnvExampleListsEveryVariable keeps packaging/edge/aether-edge.env.example,
// which packaging/edge/upgrade.json records, complete: a new variable then
// changes that file and so trips the upgrade record.
func TestEnvExampleListsEveryVariable(t *testing.T) {
	example, err := os.ReadFile("../../packaging/edge/aether-edge.env.example")
	if err != nil {
		t.Fatal(err)
	}
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	variable := regexp.MustCompile(`"(AETHER_EDGE_[A-Z0-9_]+)"`)
	for _, name := range sources {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range variable.FindAllSubmatch(src, -1) {
			if !regexp.MustCompile(`(?m)^#?` + string(m[1]) + `=`).Match(example) {
				t.Errorf("%s reads %s, which packaging/edge/aether-edge.env.example does not list", name, m[1])
			}
		}
	}
}

func TestOriginPairs(t *testing.T) {
	dir := t.TempDir()
	secret, blank := filepath.Join(dir, "github-client-secret-2"), filepath.Join(dir, "blank")
	if err := os.WriteFile(secret, []byte("fake-secret-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blank, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pair := "https://auth.example.net https://edge.example.net fake-id-2 " + secret
	o, err := parseOptions(origins, envOf(map[string]string{"AETHER_EDGE_ORIGIN_PAIRS": pair + " , https://auth3.example.net https://edge3.example.net fake-id-3 " + secret}))
	if err != nil || len(o.morePairs) != 2 || o.signinOrigin != "https://auth.example.test" || o.relayOrigin != "https://edge.example.test" {
		t.Fatalf("two more pairs: %+v %v", o, err)
	}
	if p := o.morePairs[0]; p.SigninOrigin != "https://auth.example.net" || p.RelayOrigin != "https://edge.example.net" ||
		p.GitHub.ClientID != "fake-id-2" || p.GitHub.ClientSecret != "fake-secret-2" {
		t.Fatalf("first more pair: %+v %+v", p, p.GitHub)
	}
	want := []string{"auth.example.test", "edge.example.test", "auth.example.net", "edge.example.net", "auth3.example.net", "edge3.example.net"}
	if got := certHosts(o); !slices.Equal(got, want) {
		t.Errorf("certificate hosts %v, want %v", got, want)
	}
	if err = autocert.HostWhitelist(certHosts(o)...)(t.Context(), "edge3.example.net"); err != nil {
		t.Errorf("certificate for the third pair's relay host refused: %v", err)
	}
	if o, err = parseOptions(origins, envOf(nil)); err != nil || o.morePairs != nil {
		t.Fatalf("no more pairs: %+v %v", o.morePairs, err)
	}
	if got := certHosts(o); !slices.Equal(got, want[:2]) {
		t.Errorf("certificate hosts of one pair %v, want %v", got, want[:2])
	}
	for _, tt := range []struct{ pairs, want string }{
		{"https://auth.example.net https://edge.example.net fake-id-2", "is not <signin origin> <relay origin> <GitHub client id> <client secret file>"},
		{"https://auth.example.net https://edge.example.test fake-id-2 " + secret, "has the host name edge.example.test of --relay-origin"},
		{"https://auth.example.net https://AUTH.example.net:8443 fake-id-2 " + secret, "has the host name auth.example.net of https://auth.example.net"},
		{"http://auth.example.net https://edge.example.net fake-id-2 " + secret, "--origin-pairs must be https://host[:port]"},
		{"https://auth.example.net https://edge.example.net fake-id-2 " + filepath.Join(dir, "missing"), "the client secret file of https://auth.example.net"},
		{"https://auth.example.net https://edge.example.net fake-id-2 " + blank, "holds no secret"},
		{"https://auth.example.net https://edge.example.net fake-id-2 " + secret + ",", "has an empty pair: remove the extra comma"},
	} {
		if _, err := parseOptions(append(slices.Clone(origins), "--origin-pairs", tt.pairs), envOf(nil)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("--origin-pairs %q: %v, want %q", tt.pairs, err, tt.want)
		}
	}
	var many []string
	for i := range 16 {
		many = append(many, fmt.Sprintf("https://auth%d.example.org https://edge%d.example.org fake-id %s", i, i, secret))
	}
	if _, err := parseOptions(append(slices.Clone(origins), "--origin-pairs", strings.Join(many, ",")), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "names 16 pairs, which with --signin-origin and --relay-origin make 17; an edge serves at most 16 pairs") {
		t.Errorf("17 pairs: %v", err)
	}
	if _, err := parseOptions(append(slices.Clone(devOrigins), "--dev-listen", "127.0.0.1:8080", "--origin-pairs",
		"http://127.0.0.2:8080 http://127.0.0.3:8080 fake-id-2 "+secret), envOf(nil)); err != nil {
		t.Errorf("a second loopback pair in development mode: %v", err)
	}
}

func TestRetiringPairs(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "github-client-secret")
	if err := os.WriteFile(secret, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs := "https://auth2.example.net https://edge2.example.net fake-id-2 " + secret + ",https://auth3.example.net https://edge3.example.net fake-id-3 " + secret
	parse := func(retiring string) (options, error) {
		return parseOptions(append(slices.Clone(origins), "--origin-pairs", pairs, "--retiring-pairs", retiring), envOf(nil))
	}
	o, err := parse("https://EDGE.example.test:443 https://edge2.example.net 2027-03-01, https://edge3.example.net https://edge2.example.net")
	want := []edge.Retiring{
		{RelayOrigin: "https://edge.example.test", Replacement: "https://edge2.example.net", Date: "2027-03-01"},
		{RelayOrigin: "https://edge3.example.net", Replacement: "https://edge2.example.net"},
	}
	if err != nil || !reflect.DeepEqual(o.retiring, want) {
		t.Fatalf("two retiring pairs: %+v %v, want %+v", o.retiring, err, want)
	}
	o, err = parseOptions(append(slices.Clone(origins), "--origin-pairs", pairs), envOf(map[string]string{
		"AETHER_EDGE_RETIRING_PAIRS": "https://edge2.example.net https://edge.example.test"}))
	if err != nil || !reflect.DeepEqual(o.retiring, []edge.Retiring{{RelayOrigin: "https://edge2.example.net", Replacement: "https://edge.example.test"}}) {
		t.Fatalf("AETHER_EDGE_RETIRING_PAIRS: %+v %v", o.retiring, err)
	}
	if o, err = parseOptions(append(slices.Clone(origins), "--origin-pairs", pairs), envOf(nil)); err != nil || o.retiring != nil {
		t.Fatalf("unset: %+v %v", o.retiring, err)
	}
	for _, blank := range []string{" ", "\t "} {
		if o, err = parseOptions(append(slices.Clone(origins), "--origin-pairs", pairs), envOf(map[string]string{"AETHER_EDGE_RETIRING_PAIRS": blank})); err != nil || o.retiring != nil {
			t.Fatalf("blank %q: %+v %v, want unset", blank, o.retiring, err)
		}
	}
	for _, tt := range []struct{ retiring, want string }{
		{"https://edge.example.test", `--retiring-pairs: "https://edge.example.test" is not <relay origin> <replacement relay origin> [<YYYY-MM-DD>]`},
		{"https://edge.example.test https://edge2.example.net 2027-03-01 extra", `--retiring-pairs: "https://edge.example.test https://edge2.example.net 2027-03-01 extra" is not <relay origin>`},
		{"https://edge.example.test https://edge2.example.net,", "has an empty entry: remove the extra comma"},
		{"edge.example.test https://edge2.example.net", `--retiring-pairs must be https://host[:port], not "edge.example.test"`},
		{"https://edge.example.test https://<relay-host-2>", "--retiring-pairs: edgeproto: edge url"},
		{"https://edge9.example.net https://edge2.example.net", "--retiring-pairs: https://edge9.example.net is not a relay origin of this edge; name one of --relay-origin and the relay origins of --origin-pairs"},
		{"https://edge.example.test https://edge9.example.net", "--retiring-pairs: https://edge9.example.net is not a relay origin of this edge"},
		{"https://auth2.example.net https://edge.example.test", "--retiring-pairs: https://auth2.example.net is a sign-in origin; name the relay origin of its pair, https://edge2.example.net"},
		{"https://auth.example.test https://edge2.example.net", "--retiring-pairs: https://auth.example.test is a sign-in origin; name the relay origin of its pair, https://edge.example.test"},
		{"https://edge.example.test https://EDGE.example.test", "--retiring-pairs: https://edge.example.test names its own pair as its replacement; name the relay origin of a pair that stays"},
		{"https://edge.example.test https://edge2.example.net,https://edge2.example.net https://edge3.example.net",
			"--retiring-pairs: https://edge2.example.net, the replacement of https://edge.example.test, is retiring too; name a pair that stays"},
		{"https://edge.example.test https://edge2.example.net,https://edge.example.test:443 https://edge3.example.net",
			"--retiring-pairs names the host edge.example.test in two entries; keep one entry for it"},
		{"https://edge.example.test:8443 https://edge2.example.net",
			"--retiring-pairs: https://edge.example.test:8443 has the host name of the relay origin https://edge.example.test; write that origin"},
		{"https://edge.example.test https://edge2.example.net:8443",
			"--retiring-pairs: https://edge2.example.net:8443 has the host name of the relay origin https://edge2.example.net; write that origin"},
		{"https://edge.example.test https://edge2.example.net 2027-02-30", `--retiring-pairs: the date "2027-02-30" of https://edge.example.test is not a real date written YYYY-MM-DD, such as 2027-03-01`},
		{"https://edge.example.test https://edge2.example.net 2027-3-1", `the date "2027-3-1" of https://edge.example.test is not a real date`},
		{"https://edge.example.test https://edge2.example.net 01/03/2027", `the date "01/03/2027" of https://edge.example.test is not a real date`},
	} {
		if _, err = parse(tt.retiring); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("--retiring-pairs %q: %v, want %q", tt.retiring, err, tt.want)
		}
	}
	devPairs := []string{"--dev-listen", "127.0.0.1:8080", "--signin-origin", "http://localhost:443", "--relay-origin", "http://127.0.0.1:443",
		"--origin-pairs", "http://127.0.0.2:8080 http://127.0.0.3:8080 fake-id-2 " + secret}
	if _, err = parseOptions(append(slices.Clone(devPairs), "--retiring-pairs", "http://127.0.0.1 http://127.0.0.3:8080"), envOf(nil)); err == nil ||
		!strings.Contains(err.Error(), "http://127.0.0.1 has the host name of the relay origin http://127.0.0.1:443; write that origin") {
		t.Errorf("plain HTTP origin without its port :443: %v", err)
	}
	if o, err = parseOptions(append(slices.Clone(devPairs), "--retiring-pairs", "http://127.0.0.1:443 http://127.0.0.3:8080"), envOf(nil)); err != nil ||
		!reflect.DeepEqual(o.retiring, []edge.Retiring{{RelayOrigin: "http://127.0.0.1:443", Replacement: "http://127.0.0.3:8080"}}) {
		t.Errorf("plain HTTP origin on port 443: %+v, %v", o.retiring, err)
	}
	_, err = parseOptions(append(slices.Clone(origins), "--retiring-pairs", "https://edge.example.test https://edge2.example.net"), envOf(nil))
	if err == nil || err.Error() != "--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)" {
		t.Errorf("one pair: %v", err)
	}
}

func TestRetiredPairs(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "github-client-secret")
	if err := os.WriteFile(secret, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairs := "https://auth2.example.net https://edge2.example.net fake-id-2 " + secret + ",https://auth3.example.net https://edge3.example.net fake-id-3 " + secret
	parse := func(retired string, more ...string) (options, error) {
		return parseOptions(append(append(slices.Clone(origins), "--origin-pairs", pairs, "--retired-pairs", retired), more...), envOf(nil))
	}
	o, err := parse("https://auth9.example.org:443 https://EDGE9.example.org https://edge2.example.net, https://auth8.example.org https://edge8.example.org:8443 https://edge.example.test:443")
	want := []edge.Retired{
		{SigninOrigin: "https://auth9.example.org", RelayOrigin: "https://edge9.example.org", Replacement: "https://edge2.example.net"},
		{SigninOrigin: "https://auth8.example.org", RelayOrigin: "https://edge8.example.org:8443", Replacement: "https://edge.example.test"},
	}
	if err != nil || !reflect.DeepEqual(o.retired, want) {
		t.Fatalf("two retired pairs: %+v %v, want %+v", o.retired, err, want)
	}
	o, err = parseOptions(origins, envOf(map[string]string{
		"AETHER_EDGE_RETIRED_PAIRS": "https://auth9.example.org https://edge9.example.org https://edge.example.test"}))
	if err != nil || !reflect.DeepEqual(o.retired, []edge.Retired{{SigninOrigin: "https://auth9.example.org", RelayOrigin: "https://edge9.example.org", Replacement: "https://edge.example.test"}}) {
		t.Fatalf("AETHER_EDGE_RETIRED_PAIRS with one served pair: %+v %v", o.retired, err)
	}
	if o, err = parseOptions(append(slices.Clone(origins), "--origin-pairs", pairs), envOf(nil)); err != nil || o.retired != nil {
		t.Fatalf("unset: %+v %v", o.retired, err)
	}
	for _, tt := range []struct{ retired, want string }{
		{"https://auth9.example.org https://edge9.example.org", `--retired-pairs: "https://auth9.example.org https://edge9.example.org" is not <sign-in origin> <relay origin> <replacement relay origin>`},
		{"https://auth9.example.org https://edge9.example.org https://edge2.example.net extra", "is not <sign-in origin> <relay origin> <replacement relay origin>"},
		{"https://auth9.example.org https://edge9.example.org https://edge2.example.net,", "--retired-pairs \"https://auth9.example.org https://edge9.example.org https://edge2.example.net,\" has an empty entry: remove the extra comma"},
		{"auth9.example.org https://edge9.example.org https://edge2.example.net", `--retired-pairs must be https://host[:port], not "auth9.example.org"`},
		{"https://auth9.example.org https://<relay-host-1> https://edge2.example.net", "--retired-pairs: edgeproto: edge url"},
		{"https://auth9.example.org https://edge9.example.org https://<relay-host-2>", "--retired-pairs: edgeproto: edge url"},
		{"https://auth2.example.net https://edge2.example.net https://edge3.example.net",
			"--retired-pairs: https://auth2.example.net is still served; remove its pair from --origin-pairs in the same edit"},
		{"https://auth9.example.org https://edge3.example.net:8443 https://edge2.example.net",
			"--retired-pairs: https://edge3.example.net:8443 is still served; remove its pair from --origin-pairs in the same edit"},
		{"https://auth.example.test https://edge.example.test https://edge2.example.net",
			"--retired-pairs: https://auth.example.test is still served as --signin-origin or --relay-origin; first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair"},
		{"https://auth9.example.org https://edge.example.test:8443 https://edge2.example.net", "--retired-pairs: https://edge.example.test:8443 is still served as --signin-origin or --relay-origin"},
		{"https://auth9.example.org https://AUTH9.example.org:8443 https://edge2.example.net",
			"--retired-pairs names the host auth9.example.org in two entries, https://auth9.example.org and https://AUTH9.example.org:8443; keep one entry for it"},
		{"https://auth9.example.org https://edge9.example.org https://edge2.example.net,https://auth8.example.org https://edge9.example.org:8443 https://edge2.example.net",
			"--retired-pairs names the host edge9.example.org in two entries"},
		{"https://auth9.example.org https://edge9.example.org:0443 https://edge2.example.net",
			"--retired-pairs: https://edge9.example.org:0443 cannot be named in a signed notice to servers: write a host name of at most 253 bytes and a port without leading zeros"},
		{"https://auth9.example.org:08443 https://edge9.example.org https://edge2.example.net",
			"--retired-pairs: https://auth9.example.org:08443 cannot be named in a signed notice to servers"},
		{"https://auth9.example.org https://edge9.example.org https://edge7.example.org",
			"--retired-pairs: https://edge7.example.org is not a relay origin of this edge; name one of --relay-origin and the relay origins of --origin-pairs"},
		{"https://auth9.example.org https://edge9.example.org https://edge9.example.org", "--retired-pairs: https://edge9.example.org is not a relay origin of this edge"},
		{"https://auth9.example.org https://edge9.example.org https://auth2.example.net",
			"--retired-pairs: https://auth2.example.net is a sign-in origin; name the relay origin of its pair, https://edge2.example.net"},
		{"https://auth9.example.org https://edge9.example.org https://auth.example.test",
			"--retired-pairs: https://auth.example.test is a sign-in origin; name the relay origin of its pair, https://edge.example.test"},
		{"https://[2001:db8::1] https://edge9.example.org https://edge2.example.net",
			"--retired-pairs: https://[2001:db8::1] is an IPv6 address; a retired pair is named by host names"},
		{"https://auth9.example.org https://edge9.example.org https://edge2.example.net:8443",
			"--retired-pairs: https://edge2.example.net:8443 has the host name of the relay origin https://edge2.example.net; write that origin"},
	} {
		if _, err = parse(tt.retired); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("--retired-pairs %q: %v, want %q", tt.retired, err, tt.want)
		}
	}
	_, err = parse("https://auth9.example.org https://edge9.example.org https://edge2.example.net", "--retiring-pairs", "https://edge2.example.net https://edge3.example.net")
	if want := "--retired-pairs: https://edge2.example.net, the replacement of https://edge9.example.org, is retiring; name a pair that stays"; err == nil || err.Error() != want {
		t.Errorf("replacement retiring: %v, want %q", err, want)
	}
	var many []string
	for i := range 17 {
		many = append(many, fmt.Sprintf("https://auth%d.example.org https://edge%d.example.org https://edge2.example.net", i, i))
	}
	if _, err = parse(strings.Join(many[:16], ",")); err != nil {
		t.Errorf("16 retired pairs: %v", err)
	}
	if _, err = parse(strings.Join(many, ",")); err == nil || err.Error() != "--retired-pairs names 17 pairs; an edge keeps at most 16" {
		t.Errorf("17 retired pairs: %v", err)
	}
	_, err = parse(strings.Join(many[:16], ","), "--retiring-pairs", "https://edge3.example.net https://edge2.example.net")
	if want := "--retiring-pairs and --retired-pairs name 17 pairs together, and a server is sent at most 16 notices: " +
		"retire this pair once fewer pairs are retiring or retired"; err == nil || err.Error() != want {
		t.Errorf("16 retired and 1 retiring: %v, want %q", err, want)
	}
}

// Every AETHER_EDGE_ variable is also read as CLOUDFIELD_EDGE_, which wins
// when both are set and falls back when it is empty, as serve reads them
// through envname.Getenv.
func TestEveryVariableTakesItsCloudfieldName(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("fake-secret-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pair := func(n string) string {
		return "https://auth" + n + ".example.net https://edge" + n + ".example.net fake-id-" + n + " fake-secret-" + n
	}
	// Each configuration is valid alone; the second of each pair differs
	// in every value, so the result shows which one was read.
	configs := [][2]map[string]string{{{
		"AETHER_EDGE_LISTEN": ":8443", "AETHER_EDGE_PROXY_LISTEN": "10.0.0.2:8080", "AETHER_EDGE_TRUSTED_PROXIES": "10.0.0.0/8",
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_ACME_EMAIL": "ops@example.test", "AETHER_EDGE_ACME_DIRECTORY": "https://acme.example.test/dir",
		"AETHER_EDGE_EGRESS_BUDGET": "1000", "AETHER_EDGE_OPENAI_CHALLENGE": "fake-challenge",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "fake-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET": "fake-secret",
		"AETHER_EDGE_ORIGIN_PAIRS": pair("2"), "AETHER_EDGE_DATA": "/srv/edge-a", "AETHER_EDGE_METRICS_LISTEN": "127.0.0.1:9100",
	}, {
		"AETHER_EDGE_LISTEN": ":9443", "AETHER_EDGE_PROXY_LISTEN": "10.0.0.3:8080", "AETHER_EDGE_TRUSTED_PROXIES": "10.1.0.0/16",
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.org", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.org",
		"AETHER_EDGE_ACME_EMAIL": "other@example.test", "AETHER_EDGE_ACME_DIRECTORY": "https://acme.example.org/dir",
		"AETHER_EDGE_EGRESS_BUDGET": "2000", "AETHER_EDGE_OPENAI_CHALLENGE": "other-challenge",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "other-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET": "other-secret",
		"AETHER_EDGE_ORIGIN_PAIRS": pair("3"), "AETHER_EDGE_DATA": "/srv/edge-b", "AETHER_EDGE_METRICS_LISTEN": "127.0.0.1:9200",
	}}, {{
		"AETHER_EDGE_DEV_LISTEN": "127.0.0.1:8080", "AETHER_EDGE_SIGNIN_ORIGIN": "http://localhost:8080", "AETHER_EDGE_RELAY_ORIGIN": "http://127.0.0.1:8080",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "fake-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": secretFile,
	}, {
		"AETHER_EDGE_DEV_LISTEN": "127.0.0.1:9090", "AETHER_EDGE_SIGNIN_ORIGIN": "http://localhost:9090", "AETHER_EDGE_RELAY_ORIGIN": "http://127.0.0.1:9090",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "other-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": filepath.Join(dir, "missing"),
	}}, {{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_ORIGIN_PAIRS":   "https://auth2.example.net https://edge2.example.net fake-id-2 " + secretFile,
		"AETHER_EDGE_RETIRING_PAIRS": "https://edge.example.test https://edge2.example.net 2027-03-01",
	}, {
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_ORIGIN_PAIRS":   "https://auth2.example.net https://edge2.example.net fake-id-2 " + secretFile,
		"AETHER_EDGE_RETIRING_PAIRS": "https://edge.example.test https://edge2.example.net 2027-04-01",
	}}, {{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_RETIRED_PAIRS": "https://auth9.example.org https://edge9.example.org https://edge.example.test",
	}, {
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_RETIRED_PAIRS": "https://auth8.example.org https://edge8.example.org https://edge.example.test",
	}}, {{
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_REVIEWER_ACCOUNT_FILE": filepath.Join(dir, "reviewer-a.json"),
	}, {
		"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge.example.test",
		"AETHER_EDGE_REVIEWER_ACCOUNT_FILE": filepath.Join(dir, "reviewer-b.json"),
	}}}
	source, err := os.ReadFile("options.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range regexp.MustCompile(`"(AETHER_EDGE_[A-Z_]+)"`).FindAllStringSubmatch(string(source), -1) {
		if !slices.ContainsFunc(configs, func(c [2]map[string]string) bool { _, ok := c[0][name[1]]; return ok }) {
			t.Errorf("options.go reads %s, which no configuration here sets", name[1])
		}
	}
	type parsed struct {
		o   options
		err string
	}
	parse := func(env map[string]string) parsed {
		o, err := parseOptions(nil, func(name string) string { return envname.Get(func(k string) string { return env[k] }, name) })
		return parsed{o, fmt.Sprint(err)}
	}
	for i, c := range configs {
		want, other := parse(c[0]), parse(c[1])
		if reflect.DeepEqual(want, other) {
			t.Fatalf("configuration %d: its two halves parse the same: %+v", i, want)
		}
		renamed, both, emptyNew := map[string]string{}, map[string]string{}, map[string]string{}
		for name, v := range c[0] {
			renamed[envname.New(name)] = v
			both[envname.New(name)], both[name] = v, c[1][name]
			emptyNew[envname.New(name)], emptyNew[name] = "", v
		}
		if got := parse(renamed); !reflect.DeepEqual(got, want) {
			t.Errorf("configuration %d under CLOUDFIELD_ names:\n got %+v\nwant %+v", i, got, want)
		}
		if got := parse(both); !reflect.DeepEqual(got, want) {
			t.Errorf("configuration %d with both names set, CLOUDFIELD_ must win:\n got %+v\nwant %+v", i, got, want)
		}
		if got := parse(emptyNew); !reflect.DeepEqual(got, want) {
			t.Errorf("configuration %d with empty CLOUDFIELD_ names must read AETHER_ ones:\n got %+v\nwant %+v", i, got, want)
		}
	}
}

// TestRetiredCertificates answers handshakes for retired hosts from the
// ACME cache alone: a cached certificate the client supports and that is
// unexpired, else a failed handshake, and never the autocert manager, even
// for a client that offers RSA only while the cache holds ECDSA.
func TestRetiredCertificates(t *testing.T) {
	o, err := parseOptions(append(slices.Clone(origins), "--retired-pairs",
		"https://auth9.example.org https://edge9.example.org https://edge.example.test"), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	cache := autocert.DirCache(t.TempDir())
	now := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	put := func(key string, notAfter time.Time) {
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{strings.TrimSuffix(key, "+rsa")},
			NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		data := append(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		if err = cache.Put(t.Context(), key, data); err != nil {
			t.Fatal(err)
		}
	}
	put("auth9.example.org", now.Add(24*time.Hour))
	put("edge9.example.org", now.Add(-time.Hour))
	var managed []string
	clock := now
	get := retiredCertificates(o, cache, func() time.Time { return clock }, func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		managed = append(managed, hello.ServerName)
		return &tls.Certificate{}, nil
	})
	ecdsaHello := func(name string) *tls.ClientHelloInfo {
		return &tls.ClientHelloInfo{ServerName: name, SupportedVersions: []uint16{tls.VersionTLS12},
			CipherSuites:     []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
			SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256}, SupportedCurves: []tls.CurveID{tls.CurveP256}}
	}
	rsaOnly := &tls.ClientHelloInfo{ServerName: "auth9.example.org", SupportedVersions: []uint16{tls.VersionTLS12},
		CipherSuites:     []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		SignatureSchemes: []tls.SignatureScheme{tls.PKCS1WithSHA256, tls.PSSWithSHA256}, SupportedCurves: []tls.CurveID{tls.CurveP256}}
	if cert, err := get(ecdsaHello("AUTH9.example.org")); err != nil || cert.Leaf == nil || cert.Leaf.DNSNames[0] != "auth9.example.org" {
		t.Errorf("ECDSA client at a retired host with a cached certificate: %v", err)
	}
	for name, hello := range map[string]*tls.ClientHelloInfo{"RSA-only client": rsaOnly, "expired certificate": ecdsaHello("edge9.example.org")} {
		host := hello.ServerName
		if _, err := get(hello); err == nil || err.Error() != host+" is the host of a retired pair and the ACME cache holds no unexpired certificate for it that the client supports; the edge orders none" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := get(ecdsaHello("edge.example.test")); err != nil || !slices.Equal(managed, []string{"edge.example.test"}) {
		t.Errorf("served host: %v; the manager saw %v, want only edge.example.test", err, managed)
	}
	if err := cache.Delete(t.Context(), "auth9.example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := get(ecdsaHello("auth9.example.org")); err != nil {
		t.Errorf("a certificate read once must be answered from memory after the cache entry is gone: %v", err)
	}
	put("edge9.example.org+rsa", now.Add(24*time.Hour))
	if _, err := get(ecdsaHello("edge9.example.org")); err == nil {
		t.Error("a certificate missing at the last read was read again before retiredCertRetry")
	}
	clock = now.Add(retiredCertRetry)
	if _, err := get(ecdsaHello("edge9.example.org")); err != nil {
		t.Errorf("a certificate missing at the last read was not read again after retiredCertRetry: %v", err)
	}
	clock = now.Add(25 * time.Hour)
	if _, err := get(ecdsaHello("auth9.example.org")); err == nil {
		t.Error("a certificate kept in memory was answered after it expired")
	}
	put("auth9.example.org", now.Add(48*time.Hour))
	if _, err := get(ecdsaHello("auth9.example.org")); err == nil {
		t.Error("an expired certificate was read again before retiredCertRetry")
	}
	clock = clock.Add(retiredCertRetry)
	if _, err := get(ecdsaHello("auth9.example.org")); err != nil {
		t.Errorf("an expired certificate replaced in the cache was not read again after retiredCertRetry: %v", err)
	}
}

// TestOlderImageServes reads one environment both ways: the CLOUDFIELD_EDGE_
// names this image reads first retire pair A, while the AETHER_EDGE_ names an
// older image reads alone still serve it.
func TestOlderImageServes(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "github-client-secret")
	if err := os.WriteFile(secret, []byte("fake-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, alias := filepath.Join(dir, "data"), filepath.Join(dir, "alias")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(data, alias); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "x", "y"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "x", "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "x", "y"), filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}
	newer := map[string]string{
		"CLOUDFIELD_EDGE_SIGNIN_ORIGIN": "https://auth2.example.net", "CLOUDFIELD_EDGE_RELAY_ORIGIN": "https://edge2.example.net",
		"AETHER_EDGE_RETIRED_PAIRS":    "https://auth.example.test https://edge.example.test https://edge2.example.net",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "fake-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": secret,
	}
	for _, tt := range []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"stale first pair", map[string]string{"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test", "AETHER_EDGE_RELAY_ORIGIN": "https://edge2.example.net"}, nil,
			"AETHER_EDGE_SIGNIN_ORIGIN names https://auth.example.test, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names " +
				"and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN"},
		{"stale relay origin", map[string]string{"AETHER_EDGE_RELAY_ORIGIN": "https://EDGE.example.test:8443"}, nil,
			"AETHER_EDGE_RELAY_ORIGIN names https://EDGE.example.test:8443, which --retired-pairs retires"},
		{"stale more pairs", map[string]string{"AETHER_EDGE_ORIGIN_PAIRS": "https://auth.example.test https://edge.example.test fake-id " + secret,
			"CLOUDFIELD_EDGE_ORIGIN_PAIRS": "https://auth3.example.net https://edge3.example.net fake-id " + secret}, nil,
			"AETHER_EDGE_ORIGIN_PAIRS names https://auth.example.test, which --retired-pairs retires"},
		{"aligned", map[string]string{"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth2.example.net", "AETHER_EDGE_RELAY_ORIGIN": "https://edge2.example.net"}, nil, ""},
		{"stale data directory", map[string]string{"CLOUDFIELD_EDGE_DATA": "/srv/edge", "AETHER_EDGE_DATA": "/srv/old-edge"}, nil,
			"this edge uses the data directory /srv/edge from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA " +
				"and would open /srv/old-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to /srv/edge, " +
				"or pass --data /srv/edge on the command line, which both images read"},
		{"data directory under the new name only", map[string]string{"CLOUDFIELD_EDGE_DATA": "/srv/edge"}, nil,
			"this edge uses the data directory /srv/edge from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge"},
		{"data directory aligned", map[string]string{"CLOUDFIELD_EDGE_DATA": "/srv/edge", "AETHER_EDGE_DATA": "/srv/edge/"}, nil, ""},
		{"data directory flag", map[string]string{"CLOUDFIELD_EDGE_DATA": "/srv/edge"}, []string{"--data", "/srv/edge"}, ""},
		{"data directory under another path", map[string]string{"CLOUDFIELD_EDGE_DATA": data, "AETHER_EDGE_DATA": alias}, nil, ""},
		{"data directory behind a symlink and ..", map[string]string{"CLOUDFIELD_EDGE_DATA": filepath.Join(dir, "x", "data"), "AETHER_EDGE_DATA": dir + "/up/../data"}, nil,
			"this edge uses the data directory " + filepath.Join(dir, "x", "data") + " from CLOUDFIELD_EDGE_DATA"},
		{"flag wins in both images", map[string]string{"AETHER_EDGE_SIGNIN_ORIGIN": "https://auth.example.test"},
			[]string{"--signin-origin", "https://auth2.example.net"}, ""},
	} {
		env := maps.Clone(newer)
		maps.Copy(env, tt.env)
		raw := func(k string) string { return env[k] }
		o, err := parseOptions(tt.args, func(name string) string { return envname.Get(raw, name) })
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		err = olderImageServes(o, raw)
		if (tt.want == "" && err != nil) || (tt.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.want))) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
	}
}
