package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/crypto/acme"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/edge/relay"
	edge "github.com/3xDevOps/cloudfield/internal/edge/service"
	"github.com/3xDevOps/cloudfield/internal/envname"
)

// options is the edge's configuration. Every option is a flag whose
// default comes from an AETHER_EDGE_* environment variable. The GitHub
// client secret is never a flag, because a flag's value shows in the
// process list: it comes from an environment variable or from a file named
// by a flag, such as a systemd credential.
type options struct {
	listen      string
	proxyListen string
	// trustedProxies, when set, are the networks of the proxies in front
	// of proxyListen, which may then be any address.
	trustedProxies []netip.Prefix
	devListen      string
	metricsListen  string
	dataDir        string
	signinOrigin   string
	relayOrigin    string
	acmeEmail      string
	acmeDirectory  string
	egressBudget   int64
	github         *edge.OAuthApp
	// morePairs are the origin pairs --origin-pairs names, served beside
	// --signin-origin and --relay-origin.
	morePairs []edge.Pair
	// retiring are the pairs --retiring-pairs marks retiring.
	retiring []edge.Retiring
	// retired are the pairs --retired-pairs names.
	retired []edge.Retired
	// flags are the options set on the command line.
	flags map[string]bool
	// openAIChallenge is OpenAI's domain verification token for the
	// relay host, served at edgeproto.PathOpenAIChallenge.
	openAIChallenge string
	// reviewer is the reviewer account --reviewer-account-file names.
	reviewer *edge.ReviewerAccount
}

const maxSecretFileSize = 4 << 10

func parseOptions(args []string, getenv func(string) string) (options, error) {
	var o options
	fs := flag.NewFlagSet("aether-edge serve", flag.ContinueOnError)
	env := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}
	fs.StringVar(&o.listen, "listen", env("AETHER_EDGE_LISTEN", ":443"),
		"public TLS listener, with certificates for both origins' hosts")
	fs.StringVar(&o.proxyListen, "proxy-listen", env("AETHER_EDGE_PROXY_LISTEN", ""),
		"behind a reverse proxy that terminates TLS: serve plain HTTP on this address instead of --listen, "+
			"and read client addresses from the proxy's "+relay.HeaderForwardedFor+" header; a loopback address unless --trusted-proxies names the proxies")
	trustedProxies := fs.String("trusted-proxies", env("AETHER_EDGE_TRUSTED_PROXIES", ""),
		"comma-separated networks of the reverse proxies in front of --proxy-listen when they are not on this host: "+
			"each proxy's own address, such as 10.0.0.5/32, or a network that holds only proxies, since every peer in them can set the client address. "+
			"--proxy-listen may then be any address, and requests from any other peer are refused")
	fs.StringVar(&o.devListen, "dev-listen", env("AETHER_EDGE_DEV_LISTEN", ""),
		"development mode: serve plain HTTP on this loopback address instead of --listen, without certificates")
	fs.StringVar(&o.metricsListen, "metrics-listen", metricsListenDefault(getenv),
		"loopback address that serves /metrics, /healthz and /readyz")
	fs.StringVar(&o.dataDir, "data", dataDirDefault(getenv),
		"data directory: edge.db, the signing key edge_key and the ACME cache")
	fs.StringVar(&o.signinOrigin, "signin-origin", env("AETHER_EDGE_SIGNIN_ORIGIN", ""),
		"public URL people sign in on and clients call, https://host[:port]")
	fs.StringVar(&o.relayOrigin, "relay-origin", env("AETHER_EDGE_RELAY_ORIGIN", ""),
		"public URL servers enroll with and clients connect through, https://host[:port], on another host than --signin-origin")
	fs.StringVar(&o.acmeEmail, "acme-email", env("AETHER_EDGE_ACME_EMAIL", ""),
		"contact email for the ACME account")
	fs.StringVar(&o.acmeDirectory, "acme-directory", env("AETHER_EDGE_ACME_DIRECTORY", acme.LetsEncryptURL),
		"ACME directory URL")
	budget := fs.String("egress-budget", env("AETHER_EDGE_EGRESS_BUDGET", "0"),
		"bytes the relay may send per calendar month (UTC) before it throttles every connection; 0 = no budget")
	fs.StringVar(&o.openAIChallenge, "openai-challenge", env("AETHER_EDGE_OPENAI_CHALLENGE", ""),
		"OpenAI's domain verification token for the relay host, served as text/plain at <relay origin>"+edgeproto.PathOpenAIChallenge+"; empty serves 404")
	reviewerFile := fs.String("reviewer-account-file", env("AETHER_EDGE_REVIEWER_ACCOUNT_FILE", ""),
		"reviewer account file, as aether-edge reviewer writes it: an app directory's reviewer signs in with its login and password at "+
			"<signin origin>"+edge.PathReviewerSignin+" and is offered its one server; empty serves 404 there")
	githubID := fs.String("github-client-id", env("AETHER_EDGE_GITHUB_CLIENT_ID", ""), "GitHub OAuth app client id")
	githubSecretFile := fs.String("github-client-secret-file", env("AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE", ""),
		"file holding the GitHub client secret, instead of AETHER_EDGE_GITHUB_CLIENT_SECRET")
	morePairs := fs.String("origin-pairs", env("AETHER_EDGE_ORIGIN_PAIRS", ""),
		"more origin pairs to serve beside --signin-origin and --relay-origin, comma-separated. Each is four fields separated by spaces: "+
			"<signin origin> <relay origin> <GitHub client id> <file holding that OAuth app's client secret>. "+
			"A request is served by the pair its host name belongs to; every host name of every pair differs")
	retiring := fs.String("retiring-pairs", env("AETHER_EDGE_RETIRING_PAIRS", ""),
		"address pairs that are retiring, comma-separated. Each is <relay origin> <replacement relay origin> [<YYYY-MM-DD>], both relay origins of pairs "+
			"this edge serves: every answer and page of the pair names the replacement, and the date when set. The edge never acts on the date")
	retired := fs.String("retired-pairs", env("AETHER_EDGE_RETIRED_PAIRS", ""),
		"address pairs taken out of service, comma-separated, at most 16. Each is <sign-in origin> <relay origin> <replacement relay origin>: "+
			"neither origin is served any more, every request to their hosts but /healthz gets 410 Gone naming the replacement, "+
			"and at start the edge deletes the device tokens and browser sessions issued there and those with no recorded origin")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	o.flags = map[string]bool{}
	fs.Visit(func(f *flag.Flag) { o.flags[f.Name] = true })

	var err error
	if o.egressBudget, err = strconv.ParseInt(*budget, 10, 64); err != nil || o.egressBudget < 0 {
		return options{}, fmt.Errorf("--egress-budget %q is not a byte count", *budget)
	}
	if err = checkLoopback("--metrics-listen", o.metricsListen); err != nil {
		return options{}, err
	}
	scheme := "https://"
	switch {
	case o.devListen != "" && o.proxyListen != "":
		return options{}, errors.New("--dev-listen and --proxy-listen are two ways to serve plain HTTP; set one")
	case *trustedProxies != "" && o.proxyListen == "":
		return options{}, errors.New("--trusted-proxies names the proxies in front of --proxy-listen; set --proxy-listen too")
	case *trustedProxies != "":
		if o.trustedProxies, err = parseTrustedProxies(*trustedProxies); err != nil {
			return options{}, err
		}
		if _, _, err = net.SplitHostPort(o.proxyListen); err != nil {
			return options{}, fmt.Errorf("--proxy-listen %q: %w", o.proxyListen, err)
		}
	case o.proxyListen != "":
		if err = checkLoopback("--proxy-listen", o.proxyListen); err != nil {
			return options{}, fmt.Errorf("%w, or name the proxies' networks with --trusted-proxies", err)
		}
	case o.devListen != "":
		if err = checkLoopback("--dev-listen", o.devListen); err != nil {
			return options{}, err
		}
		scheme = "http://"
	}
	for _, origin := range []struct{ flag, env, value string }{
		{"--signin-origin", "AETHER_EDGE_SIGNIN_ORIGIN", o.signinOrigin},
		{"--relay-origin", "AETHER_EDGE_RELAY_ORIGIN", o.relayOrigin},
	} {
		if origin.value == "" {
			return options{}, fmt.Errorf("%s must be %shost[:port], not \"\"; set it or %s", origin.flag, scheme, origin.env)
		}
		if err = checkOrigin(origin.flag, origin.value, scheme); err != nil {
			return options{}, err
		}
	}
	if host(o.signinOrigin) == host(o.relayOrigin) {
		return options{}, fmt.Errorf("--signin-origin %s and --relay-origin %s must name different hosts, such as auth.example.com and edge.example.com",
			o.signinOrigin, o.relayOrigin)
	}
	if *morePairs != "" {
		if o.morePairs, err = parseOriginPairs(*morePairs, scheme, o.signinOrigin, o.relayOrigin); err != nil {
			return options{}, err
		}
	}
	if strings.TrimSpace(*retiring) != "" {
		if o.retiring, err = parseRetiringPairs(*retiring, scheme, o.signinOrigin, o.relayOrigin, o.morePairs); err != nil {
			return options{}, err
		}
	}
	if *retired != "" {
		if o.retired, err = parseRetiredPairs(*retired, scheme, o.signinOrigin, o.relayOrigin, o.morePairs, o.retiring); err != nil {
			return options{}, err
		}
	}
	if *reviewerFile != "" {
		if o.reviewer, err = edge.LoadReviewerAccount(*reviewerFile); err != nil {
			return options{}, err
		}
	}
	if o.github, err = githubApp(*githubID, getenv("AETHER_EDGE_GITHUB_CLIENT_SECRET"), *githubSecretFile); err != nil {
		return options{}, err
	}
	return o, nil
}

// checkOrigin checks that value, the origin flagName names, is an origin
// of scheme.
func checkOrigin(flagName, value, scheme string) error {
	if !strings.HasPrefix(value, scheme) {
		if scheme == "http://" {
			return fmt.Errorf("--dev-listen serves plain HTTP, so %s must be http://<loopback host>:<port>, not %q", flagName, value)
		}
		return fmt.Errorf("%s must be https://host[:port], not %q; for a local plain-HTTP edge use --dev-listen", flagName, value)
	}
	if _, err := edgeproto.Origin(value); err != nil {
		return fmt.Errorf("%s: %w", flagName, err)
	}
	return nil
}

// parseOriginPairs parses --origin-pairs. Every host name of every pair,
// signin's and relay's among them, must differ.
func parseOriginPairs(list, scheme, signin, relay string) ([]edge.Pair, error) {
	hosts := map[string]string{host(signin): "--signin-origin", host(relay): "--relay-origin"}
	var pairs []edge.Pair
	for entry := range strings.SplitSeq(list, ",") {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			return nil, fmt.Errorf("--origin-pairs %q has an empty pair: remove the extra comma", list)
		}
		if len(fields) != 4 {
			return nil, fmt.Errorf("--origin-pairs: %q is not <signin origin> <relay origin> <GitHub client id> <client secret file>", strings.TrimSpace(entry))
		}
		for _, origin := range fields[:2] {
			if err := checkOrigin("--origin-pairs", origin, scheme); err != nil {
				return nil, err
			}
			h := host(origin)
			if other, ok := hosts[h]; ok {
				return nil, fmt.Errorf("--origin-pairs: %s has the host name %s of %s; each origin of each pair needs its own host name", origin, h, other)
			}
			hosts[h] = origin
		}
		secret, err := readSecretFile(fields[3])
		if err != nil {
			return nil, fmt.Errorf("--origin-pairs: the client secret file of %s: %w", fields[0], err)
		}
		if secret == "" {
			return nil, fmt.Errorf("--origin-pairs: %s, the client secret file of %s, holds no secret", fields[3], fields[0])
		}
		pairs = append(pairs, edge.Pair{SigninOrigin: fields[0], RelayOrigin: fields[1],
			GitHub: &edge.OAuthApp{ClientID: fields[2], ClientSecret: secret}})
	}
	if len(pairs)+1 > edgeproto.MaxRelayOrigins {
		return nil, fmt.Errorf("--origin-pairs names %d pairs, which with --signin-origin and --relay-origin make %d; an edge serves at most %d pairs",
			len(pairs), len(pairs)+1, edgeproto.MaxRelayOrigins)
	}
	return pairs, nil
}

// parseRetiringPairs parses --retiring-pairs against the served pairs:
// signin and relay are the first pair's, more --origin-pairs'.
func parseRetiringPairs(list, scheme, signin, relay string, more []edge.Pair) ([]edge.Retiring, error) {
	if len(more) == 0 {
		return nil, errors.New("--retiring-pairs: this edge serves one address pair, which cannot retire; " +
			"serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)")
	}
	relays, signinHosts := servedHosts(signin, relay, more)
	served := func(origin string) (string, error) {
		return servedRelay("--retiring-pairs", origin, scheme, relays, signinHosts)
	}
	var out []edge.Retiring
	for entry := range strings.SplitSeq(list, ",") {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			return nil, fmt.Errorf("--retiring-pairs %q has an empty entry: remove the extra comma", list)
		}
		if len(fields) > 3 || len(fields) < 2 {
			return nil, fmt.Errorf("--retiring-pairs: %q is not <relay origin> <replacement relay origin> [<YYYY-MM-DD>]", strings.TrimSpace(entry))
		}
		r := edge.Retiring{}
		var err error
		if r.RelayOrigin, err = served(fields[0]); err != nil {
			return nil, err
		}
		if r.Replacement, err = served(fields[1]); err != nil {
			return nil, err
		}
		if r.RelayOrigin == r.Replacement {
			return nil, fmt.Errorf("--retiring-pairs: %s names its own pair as its replacement; name the relay origin of a pair that stays", fields[0])
		}
		if len(fields) == 3 {
			if !edgeproto.ValidDate(fields[2]) {
				return nil, fmt.Errorf("--retiring-pairs: the date %q of %s is not a real date written YYYY-MM-DD, such as 2027-03-01", fields[2], fields[0])
			}
			r.Date = fields[2]
		}
		for _, o := range out {
			if o.RelayOrigin == r.RelayOrigin {
				return nil, fmt.Errorf("--retiring-pairs names the host %s in two entries; keep one entry for it", host(r.RelayOrigin))
			}
		}
		out = append(out, r)
	}
	for _, r := range out {
		for _, o := range out {
			if r.Replacement == o.RelayOrigin {
				return nil, fmt.Errorf("--retiring-pairs: %s, the replacement of %s, is retiring too; name a pair that stays", r.Replacement, r.RelayOrigin)
			}
		}
	}
	return out, nil
}

// servedHosts maps the host names of the served relay origins to those
// origins, and the host names of the served sign-in origins to their
// pairs' relay origins; signin and relay are the first pair's.
func servedHosts(signin, relay string, more []edge.Pair) (relays, signinHosts map[string]string) {
	relays, signinHosts = map[string]string{host(relay): relay}, map[string]string{host(signin): relay}
	for _, p := range more {
		relays[host(p.RelayOrigin)] = p.RelayOrigin
		signinHosts[host(p.SigninOrigin)] = p.RelayOrigin
	}
	return relays, signinHosts
}

// servedRelay is origin, which flagName names, as the served relay origin
// it is.
func servedRelay(flagName, origin, scheme string, relays, signinHosts map[string]string) (string, error) {
	if err := checkOrigin(flagName, origin, scheme); err != nil {
		return "", err
	}
	r, ok := relays[host(origin)]
	switch {
	case ok && edge.CanonicalOrigin(r) != edge.CanonicalOrigin(origin):
		return "", fmt.Errorf("%s: %s has the host name of the relay origin %s; write that origin", flagName, origin, r)
	case ok:
		c, _ := edgeproto.Origin(r) // parseOptions checked it
		return c, nil
	}
	if r, ok := signinHosts[host(origin)]; ok {
		return "", fmt.Errorf("%s: %s is a sign-in origin; name the relay origin of its pair, %s", flagName, origin, r)
	}
	return "", fmt.Errorf("%s: %s is not a relay origin of this edge; name one of --relay-origin and the relay origins of --origin-pairs", flagName, origin)
}

// parseRetiredPairs parses --retired-pairs against the served pairs,
// signin and relay the first one's, more --origin-pairs', and the
// retiring ones.
func parseRetiredPairs(list, scheme, signin, relay string, more []edge.Pair, retiring []edge.Retiring) ([]edge.Retired, error) {
	relays, signinHosts := servedHosts(signin, relay, more)
	var out []edge.Retired
	hosts := map[string]string{}
	for entry := range strings.SplitSeq(list, ",") {
		fields := strings.Fields(entry)
		if len(fields) == 0 {
			return nil, fmt.Errorf("--retired-pairs %q has an empty entry: remove the extra comma", list)
		}
		if len(fields) != 3 {
			return nil, fmt.Errorf("--retired-pairs: %q is not <sign-in origin> <relay origin> <replacement relay origin>", strings.TrimSpace(entry))
		}
		for _, origin := range fields[:2] {
			if err := checkOrigin("--retired-pairs", origin, scheme); err != nil {
				return nil, err
			}
			h := host(origin)
			if a, err := netip.ParseAddr(h); err == nil && a.Is6() {
				return nil, fmt.Errorf("--retired-pairs: %s is an IPv6 address; a retired pair is named by host names", origin)
			}
			_, isRelay := relays[h]
			_, isSignin := signinHosts[h]
			switch {
			case h == host(signin) || h == host(relay):
				return nil, fmt.Errorf("--retired-pairs: %s is still served as --signin-origin or --relay-origin; "+
					"first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair", origin)
			case isRelay || isSignin:
				return nil, fmt.Errorf("--retired-pairs: %s is still served; remove its pair from --origin-pairs in the same edit", origin)
			}
			if other, ok := hosts[h]; ok {
				return nil, fmt.Errorf("--retired-pairs names the host %s in two entries, %s and %s; keep one entry for it", h, other, origin)
			}
			hosts[h] = origin
		}
		replacement, err := servedRelay("--retired-pairs", fields[2], scheme, relays, signinHosts)
		if err != nil {
			return nil, err
		}
		for _, r := range retiring {
			if r.RelayOrigin == replacement {
				return nil, fmt.Errorf("--retired-pairs: %s, the replacement of %s, is retiring; name a pair that stays", replacement, fields[1])
			}
		}
		out = append(out, edge.Retired{SigninOrigin: edge.CanonicalOrigin(fields[0]), RelayOrigin: edge.CanonicalOrigin(fields[1]), Replacement: replacement})
	}
	if len(out) > edgeproto.MaxRelayOrigins {
		return nil, fmt.Errorf("--retired-pairs names %d pairs; an edge keeps at most %d", len(out), edgeproto.MaxRelayOrigins)
	}
	if n := len(out) + len(retiring); n > edgeproto.MaxRelayOrigins {
		return nil, fmt.Errorf("--retiring-pairs and --retired-pairs name %d pairs together, and a server is sent at most %d notices: "+
			"retire this pair once fewer pairs are retiring or retired", n, edgeproto.MaxRelayOrigins)
	}
	return out, nil
}

// host is the lowercase host name of a valid origin.
func host(origin string) string {
	u, _ := url.Parse(origin) // parseOptions checked it
	return strings.ToLower(u.Hostname())
}

// dataDirDefault is the default of --data: AETHER_EDGE_DATA, else the
// unit's state directory.
func dataDirDefault(getenv func(string) string) string {
	if d := getenv("AETHER_EDGE_DATA"); d != "" {
		return d
	}
	return "/var/lib/aether-edge"
}

// parseTrustedProxies parses --trusted-proxies. An IPv4-mapped network
// is kept as the IPv4 network it maps, since peers are compared unmapped.
func parseTrustedProxies(list string) ([]netip.Prefix, error) {
	var proxies []netip.Prefix
	for field := range strings.SplitSeq(list, ",") {
		field = strings.TrimSpace(field)
		p, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("--trusted-proxies: %q is not a network such as 172.18.0.0/16 or 10.0.0.5/32", field)
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("--trusted-proxies: %q trusts every address, so any client could choose the address the edge limits it by; name the proxies' own network", field)
		}
		proxies = append(proxies, p.Masked())
	}
	return proxies, nil
}

// metricsListenDefault is the default of --metrics-listen, shared with
// aether-edge healthcheck.
func metricsListenDefault(getenv func(string) string) string {
	if a := getenv("AETHER_EDGE_METRICS_LISTEN"); a != "" {
		return a
	}
	return "127.0.0.1:9464"
}

// checkLoopback refuses an address that is not an IP loopback address and
// port. A host name is refused too: it may resolve elsewhere.
func checkLoopback(flagName, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q: %w", flagName, addr, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("%s %q must be a loopback address such as 127.0.0.1:8080", flagName, addr)
	}
	return nil
}

// githubApp returns the GitHub OAuth app, or nil when it has neither a
// client id nor a secret. The secret comes from env or from secretFile,
// not both. A secret file holding only white space holds no secret: the
// systemd unit gives the edge such a file when the real one is missing.
func githubApp(clientID, envSecret, secretFile string) (*edge.OAuthApp, error) {
	if envSecret != "" && secretFile != "" {
		return nil, errors.New("both AETHER_EDGE_GITHUB_CLIENT_SECRET and --github-client-secret-file are set; keep one")
	}
	secret := envSecret
	if secretFile != "" {
		var err error
		if secret, err = readSecretFile(secretFile); err != nil {
			return nil, fmt.Errorf("--github-client-secret-file: %w", err)
		}
	}
	switch {
	case clientID == "" && secret == "":
		return nil, nil
	case clientID == "":
		return nil, errors.New("a github client secret is set but --github-client-id is not")
	case secret == "" && secretFile != "":
		return nil, fmt.Errorf("--github-client-id is set but %s, named by --github-client-secret-file, holds no secret", secretFile)
	case secret == "":
		return nil, errors.New("--github-client-id is set but its secret is not; name a file holding it with AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE " +
			"(--github-client-secret-file), or set AETHER_EDGE_GITHUB_CLIENT_SECRET")
	}
	return &edge.OAuthApp{ClientID: clientID, ClientSecret: secret}, nil
}

func readSecretFile(path string) (string, error) {
	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%w; put the secret there, or name the file that holds it", err)
	case errors.Is(err, fs.ErrPermission):
		return "", fmt.Errorf("%w; make it readable by uid %d, the user aether-edge runs as", err, os.Geteuid())
	case err != nil:
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, maxSecretFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxSecretFileSize {
		return "", fmt.Errorf("%s is larger than %d bytes", path, maxSecretFileSize)
	}
	return strings.TrimSpace(string(data)), nil
}

// olderImageServes refuses a retired host that an edge image older than
// v0.8.7 would serve: such an image reads the origins only from the
// AETHER_EDGE_ names, so one that a CLOUDFIELD_EDGE_ name shadows here is
// still what it serves after a rollback. getenv reads the environment
// without the CLOUDFIELD_EDGE_ names.
func olderImageServes(o options, getenv func(string) string) error {
	retired := map[string]bool{}
	for _, r := range o.retired {
		retired[host(r.SigninOrigin)], retired[host(r.RelayOrigin)] = true, true
	}
	if len(retired) == 0 {
		return nil
	}
	for _, v := range []struct{ flag, name string }{
		{"signin-origin", "AETHER_EDGE_SIGNIN_ORIGIN"}, {"relay-origin", "AETHER_EDGE_RELAY_ORIGIN"}, {"origin-pairs", "AETHER_EDGE_ORIGIN_PAIRS"},
	} {
		value := getenv(v.name)
		if o.flags[v.flag] || value == "" {
			continue
		}
		for entry := range strings.SplitSeq(value, ",") {
			fields := strings.Fields(entry)
			if v.name != "AETHER_EDGE_ORIGIN_PAIRS" {
				fields = []string{strings.TrimSpace(entry)}
			}
			for _, origin := range fields[:min(2, len(fields))] {
				u, err := url.Parse(origin)
				if err != nil || !retired[strings.ToLower(u.Hostname())] {
					continue
				}
				return fmt.Errorf("%s names %s, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names "+
					"and would serve it in full after a rollback. Remove %s, or set it to the value of %s", v.name, origin, v.name, envname.New(v.name))
			}
		}
	}
	return nil
}
