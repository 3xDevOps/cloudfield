// Command aether-edge is an edge: the sign-in service and the relay that
// Aether servers and clients both dial out to. docs/edge.md describes
// running one.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/3xDevOps/cloudfield/internal/edge/relay"
	edge "github.com/3xDevOps/cloudfield/internal/edge/service"
	"github.com/3xDevOps/cloudfield/internal/envname"
	"github.com/3xDevOps/cloudfield/internal/selfupdate"
)

// shutdownTimeout bounds a clean stop: drain to every server, then the
// HTTP servers. It stays below the 30 seconds docs/edge.md gives the
// container runtime to stop the edge before it kills it.
const shutdownTimeout = 20 * time.Second

// commands are aether-edge's top-level commands.
var commands = map[string]func(args []string) error{
	"version":     func(args []string) error { return versionCmd(args, os.Stdout) },
	"serve":       serve,
	"healthcheck": func(args []string) error { return healthcheck(args, envname.Getenv, os.Stdout) },
	"servers":     func(args []string) error { return servers(args, envname.Getenv, os.Stdout) },
	"accounts":    func(args []string) error { return accounts(args, envname.Getenv, os.Stdout) },
	"retiring":    func(args []string) error { return retiring(args, envname.Getenv, os.Stdout) },
	"schema":      func(args []string) error { return schemaCmd(args, envname.Getenv, os.Stdout) },
	"backup":      func(args []string) error { return backupCmd(args, envname.Getenv, os.Stdout, os.Stderr) },
	"restore":     func(args []string) error { return restoreCmd(args, envname.Getenv, os.Stdout) },
	"reviewer":    func(args []string) error { return reviewerCmd(args, os.Stdout) },
	"release-check": func(args []string) error {
		return releaseCheck(args, func(ctx context.Context, tag string) (selfupdate.Manifest, []byte, []byte, error) {
			return selfupdate.FetchSignedManifestFrom(ctx, "https://github.com/"+selfupdate.Repo, tag)
		}, os.Stdout)
	},
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, ok := commands[os.Args[1]]
	if !ok {
		_, _ = fmt.Fprintf(os.Stderr, "aether-edge: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	exit(cmd(os.Args[2:]))
}

func exit(err error) {
	var hold *holdError
	switch {
	case errors.As(err, &hold):
		for _, reason := range hold.reasons {
			_, _ = fmt.Fprintln(os.Stderr, "aether-edge: hold:", reason)
		}
		os.Exit(3)
	case err == nil, errors.Is(err, flag.ErrHelp):
	case errors.Is(err, errUsage):
		os.Exit(2)
	default:
		_, _ = fmt.Fprintln(os.Stderr, "aether-edge:", err)
		os.Exit(1)
	}
}

func usage() {
	_, _ = fmt.Fprint(os.Stderr, `usage: aether-edge <command>

commands:
  serve          run the edge (aether-edge serve -h lists its options)
  healthcheck    exit 0 when the edge on this machine answers (--ready: is ready), 1 with the reason
  servers        list, remove, block and unblock servers
  accounts       list, block, unblock and delete accounts
  retiring       who the edge last saw on each origin pair: retiring [--origin <relay origin>] [--json]
  schema         print the database's schema version and compat floor [--json]
  backup         copy the database while the edge runs: backup [--with-key] <file|dir|->
  restore        replace the database of a stopped edge: restore [--key <file>] <file>
  reviewer       write a reviewer account file with a new password: reviewer --login <login> --server-id <id> <file>
  release-check  verify a release's signed edge image; exit 3 when its upgrade must hold
  version        print the version [--json]
`)
}

func serve(args []string) (err error) {
	o, err := parseOptions(args, envname.Getenv)
	if err != nil {
		return err
	}
	if err = olderImageServes(o, os.Getenv); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err = checkDataDir(o.dataDir); err != nil {
		return err
	}
	unlock, err := lockPath(filepath.Join(o.dataDir, lockFile), exclusive)
	if errors.Is(err, errLocked) {
		return fmt.Errorf("%w; an edge already serves %s, or a restore or an operator command runs on it: one edge per data directory", err, o.dataDir)
	}
	if err != nil {
		return err
	}
	defer unlock()
	unlockDB, err := lockPath(filepath.Join(o.dataDir, dbLockFile), shared)
	if err != nil {
		return err
	}
	defer unlockDB()
	if err = removeStaleKeys(o.dataDir); err != nil {
		return err
	}

	svc, err := edge.New(edge.Config{
		DataDir: o.dataDir, SigninOrigin: o.signinOrigin, RelayOrigin: o.relayOrigin,
		GitHub: o.github, MorePairs: o.morePairs, Retiring: o.retiring, Retired: o.retired, OpenAIChallenge: o.openAIChallenge, Reviewer: o.reviewer,
		RecordOrigins: len(o.retiring) > 0,
	})
	if err != nil {
		return err
	}
	// Deferred, the store closes once the relay and the HTTP servers have
	// stopped using it.
	defer func() { err = errors.Join(err, svc.Close()) }()
	// Not ctx: a stop signal during startup ends the edge through the
	// shutdown below, not with the load it interrupted.
	rl, err := relay.New(context.Background(), svc.RelayConfig(o.egressBudget))
	if err != nil {
		return err
	}
	svc.SetLink(rl)

	// No read or write timeout: relayed streams are long-lived WebSockets.
	web := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		// net/http answers OPTIONS * itself, on any host, before Handler.
		DisableGeneralOptionsHandler: len(o.retired) > 0,
		ErrorLog:                     slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
	}
	var listener listenerState
	metrics := &http.Server{Handler: metricsHandler(svc, rl, &listener), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 3)
	metricsLn, err := net.Listen("tcp", o.metricsListen)
	if err != nil {
		return fmt.Errorf("metrics listener: %w", err)
	}

	var public, edgeLn net.Listener
	var serveEdge func() error
	switch {
	case o.devListen != "":
		var ln net.Listener
		if ln, err = net.Listen("tcp", o.devListen); err != nil {
			return fmt.Errorf("development listener: %w", err)
		}
		slog.Warn("aether-edge: development mode: plain HTTP, no certificates", "listen", ln.Addr())
		edgeLn, serveEdge = ln, func() error { return listener.serve("edge", web, ln) }
	case o.proxyListen != "":
		var ln net.Listener
		if ln, err = net.Listen("tcp", o.proxyListen); err != nil {
			return fmt.Errorf("proxy listener: %w", err)
		}
		web.Handler = rl.Forwarded(o.trustedProxies, web.Handler)
		proxies := "loopback"
		if o.trustedProxies != nil {
			proxies = fmt.Sprint(o.trustedProxies)
		}
		slog.Info("aether-edge: behind a reverse proxy: plain HTTP, client addresses from "+relay.HeaderForwardedFor,
			"listen", ln.Addr(), "proxies", proxies)
		edgeLn, serveEdge = ln, func() error { return listener.serve("edge", web, ln) }
	default:
		tlsConfig := edgeTLS(o)
		if public, err = net.Listen("tcp", o.listen); errors.Is(err, fs.ErrPermission) {
			return fmt.Errorf("public listener: %w; ports below 1024 need a privilege this user lacks: "+
				"listen on a higher port such as :8443 (--listen, AETHER_EDGE_LISTEN) and forward 443 to it", err)
		} else if err != nil {
			return fmt.Errorf("public listener: %w", err)
		}
		edgeLn, serveEdge = public, func() error { return listener.serve("edge", web, tls.NewListener(rl.Listener(public), tlsConfig)) }
	}
	// Recording begins with the listeners bound and before the first
	// request, so every request it records falls in a recorded run, and a
	// start that fails to bind records no period.
	if err = svc.StartRecording(context.Background()); err != nil {
		return errors.Join(err, edgeLn.Close(), metricsLn.Close())
	}
	go func() { errc <- serveEdge() }()
	// The metrics listener serves /healthz, so it answers only once the
	// edge's own listener is up.
	go func() { errc <- serveHTTP("metrics", metrics, metricsLn) }()
	slog.Info("aether-edge: serving", "signin", o.signinOrigin, "relay", o.relayOrigin, "metrics", metricsLn.Addr())
	for _, p := range o.morePairs {
		slog.Info("aether-edge: serving", "signin", p.SigninOrigin, "relay", p.RelayOrigin)
	}
	for _, r := range o.retiring {
		slog.Info("aether-edge: retiring", "relay", r.RelayOrigin, "replacement", r.Replacement, "date", r.Date)
	}
	for _, r := range o.retired {
		slog.Info("aether-edge: retired", "signin", r.SigninOrigin, "relay", r.RelayOrigin, "replacement", r.Replacement)
	}
	if o.reviewer != nil {
		slog.Warn("aether-edge: reviewer account on", "login", o.reviewer.Login, "server", o.reviewer.ServerID,
			"signin", o.signinOrigin+edge.PathReviewerSignin)
	}

	select {
	case <-ctx.Done():
		err = nil
	case err = <-errc:
	}
	// A second signal during the drain kills the edge.
	stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Drain first: /readyz answers draining from here on, and servers hear
	// the edge is stopping and when to reconnect instead of reading a
	// dropped socket as an outage.
	err = errors.Join(err, rl.Shutdown(shutdownCtx))
	if public != nil {
		err = errors.Join(err, ignoreClosed(public.Close()))
	}
	return errors.Join(err, web.Shutdown(shutdownCtx), metrics.Shutdown(shutdownCtx))
}

// edgeTLS obtains certificates for exactly the edge's host names with
// TLS-ALPN-01.
func edgeTLS(o options) *tls.Config {
	cache := autocert.DirCache(filepath.Join(o.dataDir, "acme"))
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      cache,
		HostPolicy: autocert.HostWhitelist(certHosts(o)...),
		Email:      o.acmeEmail,
		Client:     &acme.Client{DirectoryURL: o.acmeDirectory},
	}
	cfg := m.TLSConfig()
	cfg.MinVersion = tls.VersionTLS12
	cfg.GetCertificate = retiredCertificates(o, cache, time.Now, cfg.GetCertificate)
	return cfg
}

// certHosts are the host names of the served pairs, which the edge
// obtains certificates for.
func certHosts(o options) []string {
	hosts := []string{host(o.signinOrigin), host(o.relayOrigin)}
	for _, p := range o.morePairs {
		hosts = append(hosts, host(p.SigninOrigin), host(p.RelayOrigin))
	}
	return hosts
}

// retiredCertRetry is the least time before a retired host's certificate
// that the cache did not hold is read again.
const retiredCertRetry = 5 * time.Minute

// retiredCertificates answers a handshake for a retired pair's host
// with a certificate from cache that is valid at now and that the client
// supports, and fails it when there is none; any other host goes to next.
// A retired host never reaches the autocert manager, which would order a
// certificate for it, as it does for a key type it has none of. A
// certificate read from cache stays in memory; a missing one is read again
// after retiredCertRetry, so handshakes do not each read the disk.
func retiredCertificates(o options, cache autocert.Cache, now func() time.Time,
	next func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	retired := map[string]bool{}
	for _, r := range o.retired {
		retired[host(r.SigninOrigin)], retired[host(r.RelayOrigin)] = true, true
	}
	type loaded struct {
		cert *tls.Certificate
		at   time.Time
	}
	var mu sync.Mutex
	certs := map[string]loaded{}
	load := func(key string) *tls.Certificate {
		mu.Lock()
		defer mu.Unlock()
		if l, ok := certs[key]; ok && (l.cert != nil || now().Sub(l.at) < retiredCertRetry) {
			return l.cert
		}
		l := loaded{at: now()}
		if data, err := cache.Get(context.Background(), key); err == nil {
			if cert, err := tls.X509KeyPair(data, data); err == nil && cert.Leaf != nil {
				l.cert = &cert
			}
		}
		certs[key] = l
		return l.cert
	}
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
		if !retired[name] {
			return next(hello)
		}
		for _, key := range []string{name, name + "+rsa"} {
			cert := load(key)
			if cert == nil || now().Before(cert.Leaf.NotBefore) || !now().Before(cert.Leaf.NotAfter) {
				continue
			}
			if hello.SupportsCertificate(cert) == nil {
				return cert, nil
			}
		}
		return nil, fmt.Errorf("%s is the host of a retired pair and the ACME cache holds no unexpired certificate for it that the client supports; the edge orders none", name)
	}
}

func serveHTTP(name string, srv *http.Server, ln net.Listener) error {
	err := srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("%s server: %w", name, err)
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
