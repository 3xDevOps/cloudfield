package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/edge/relay"
	edge "github.com/3xDevOps/cloudfield/internal/edge/service"
)

// TestMain makes the test binary aether-edge itself when
// AETHER_EDGE_TEST_MAIN is set, so a test can run the edge as a process
// of its own, as a container does.
func TestMain(m *testing.M) {
	if os.Getenv("AETHER_EDGE_TEST_MAIN") != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const fakeSecret = "fake-client-secret-for-tests"

// edgeProc is an aether-edge serve process in development mode.
type edgeProc struct {
	cmd     *exec.Cmd
	stderr  *lockedBuffer
	done    chan struct{}
	listen  string
	metrics string
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// edgeCommand runs aether-edge with only env for its environment, in dir,
// with HOME and TMPDIR also pointing at dir.
func edgeCommand(dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append([]string{"AETHER_EDGE_TEST_MAIN=1", "HOME=" + dir, "TMPDIR=" + dir}, env...)
	return cmd
}

func startEdge(t *testing.T, dataDir, emptyDir, secretFile string) *edgeProc {
	t.Helper()
	return startEdgeCommand(t, serveCommand(dataDir, emptyDir, secretFile))
}

// serveCommand is aether-edge serve in development mode.
func serveCommand(dataDir, emptyDir, secretFile string) *exec.Cmd {
	return edgeCommand(emptyDir, []string{
		"AETHER_EDGE_DATA=" + dataDir,
		"AETHER_EDGE_DEV_LISTEN=127.0.0.1:0",
		"AETHER_EDGE_METRICS_LISTEN=127.0.0.1:0",
		"AETHER_EDGE_SIGNIN_ORIGIN=http://localhost:8080",
		"AETHER_EDGE_RELAY_ORIGIN=http://127.0.0.1:8080",
		"AETHER_EDGE_GITHUB_CLIENT_ID=fake-client-id",
		"AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=" + secretFile,
	}, "serve")
}

func startEdgeCommand(t *testing.T, cmd *exec.Cmd) *edgeProc {
	t.Helper()
	p := &edgeProc{cmd: cmd, stderr: &lockedBuffer{}, done: make(chan struct{})}
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = p.cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		<-p.done
	})
	listen := regexp.MustCompile(`development mode.* listen=(\S+)`)
	metrics := regexp.MustCompile(`aether-edge: serving .* metrics=(\S+)`)
	deadline := time.After(30 * time.Second)
	for {
		out := p.stderr.String()
		if m := metrics.FindStringSubmatch(out); m != nil {
			p.metrics = m[1]
			p.listen = listen.FindStringSubmatch(out)[1]
			return p
		}
		select {
		case <-p.done:
			t.Fatalf("aether-edge serve exited: %s\n%s", p.cmd.ProcessState, out)
		case <-deadline:
			t.Fatalf("aether-edge serve did not start:\n%s", out)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// fingerprint reads the edge key's fingerprint from PathEdgeInfo on the
// relay origin, as a server does.
func (p *edgeProc) fingerprint(t *testing.T) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+p.listen+edgeproto.PathEdgeInfo, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "127.0.0.1:8080"
	req.Header.Set(edgeproto.HeaderVersion, strconv.Itoa(edgeproto.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only
	var info edgeproto.EdgeInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Fingerprint == "" {
		t.Fatalf("GET %s: %s %v", edgeproto.PathEdgeInfo, resp.Status, err)
	}
	return info.Fingerprint
}

// stop sends SIGTERM, as a container runtime does, and wants exit status
// 0 within the shutdown timeout.
func (p *edgeProc) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatalf("aether-edge serve still runs %s after SIGTERM:\n%s", shutdownTimeout+5*time.Second, p.stderr.String())
	}
	if code := p.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("aether-edge serve exited %d after SIGTERM:\n%s", code, p.stderr.String())
	}
}

func healthcheckProc(dir, metrics string) (string, error) {
	out, err := edgeCommand(dir, []string{"AETHER_EDGE_METRICS_LISTEN=" + metrics}, "healthcheck").CombinedOutput()
	return string(out), err
}

// TestServeAsAContainerRunsIt starts, checks, stops and replaces the edge
// as a container runtime does, with an empty working directory, HOME and
// TMPDIR that it must not write to.
func TestServeAsAContainerRunsIt(t *testing.T) {
	empty, volume := t.TempDir(), filepath.Join(t.TempDir(), "data")
	secretFile := filepath.Join(t.TempDir(), "github-client-secret")
	if err := os.WriteFile(secretFile, []byte(fakeSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(empty, 0o555); err != nil {
		t.Fatal(err)
	}

	first := startEdge(t, volume, empty, secretFile)
	fingerprint := first.fingerprint(t)
	if out, err := healthcheckProc(empty, first.metrics); err != nil {
		t.Fatalf("healthcheck of a running edge: %v\n%s", err, out)
	}
	resp, err := http.Get("http://" + first.metrics + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(metrics), "\naether_edge_draining 0\n") {
		t.Errorf("/metrics of a running edge:\n%s", metrics)
	}
	keyPath := filepath.Join(volume, "edge_key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(keyPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("signing key: %v %v, want mode 0600", info, err)
	}
	first.stop(t)
	for what, text := range map[string]string{"the log": first.stderr.String(), "/metrics": string(metrics)} {
		if strings.Contains(text, fakeSecret) {
			t.Errorf("the client secret is in %s", what)
		}
	}
	out, err := healthcheckProc(empty, first.metrics)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, "aether-edge: healthcheck: no edge answers on ") {
		t.Fatalf("healthcheck of a stopped edge: %v\n%s", err, out)
	}

	// A replacement with the same data directory presents the same key,
	// and never rewrites it; one with an empty directory makes another.
	second := startEdge(t, volume, empty, secretFile)
	if got := second.fingerprint(t); got != fingerprint {
		t.Errorf("same data directory: fingerprint %s, want %s", got, fingerprint)
	}
	second.stop(t)
	if again, err := os.ReadFile(keyPath); err != nil || !bytes.Equal(again, key) {
		t.Errorf("signing key file changed on the second start: %v", err)
	}
	fresh := startEdge(t, filepath.Join(t.TempDir(), "data"), empty, secretFile)
	if got := fresh.fingerprint(t); got == fingerprint {
		t.Errorf("new data directory kept the fingerprint %s", got)
	}
	fresh.stop(t)

	if entries, err := os.ReadDir(empty); err != nil || len(entries) != 0 {
		t.Errorf("the edge wrote outside its data directory: %v %v", entries, err)
	}
}

func TestServeWithoutConfigurationSaysWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	out, err := edgeCommand(dir, []string{"AETHER_EDGE_DATA=" + filepath.Join(dir, "data")}, "serve").CombinedOutput()
	var exit *exec.ExitError
	want := `aether-edge: --signin-origin must be https://host[:port], not ""; set it or AETHER_EDGE_SIGNIN_ORIGIN` + "\n"
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || string(out) != want {
		t.Fatalf("serve with no configuration: %v\n%s", err, out)
	}
}

// TestServeHelpDescribesTheProxyOptions checks that the help of the proxy
// options matches both modes: a proxy on this host, and trusted proxies
// elsewhere.
func TestServeHelpDescribesTheProxyOptions(t *testing.T) {
	out, _ := edgeCommand(t.TempDir(), nil, "serve", "-h").CombinedOutput()
	help := strings.Join(strings.Fields(string(out)), " ")
	for _, want := range []string{
		"serve plain HTTP on this address instead of --listen, and read client addresses from the proxy's X-Forwarded-For header; " +
			"a loopback address unless --trusted-proxies names the proxies",
		"each proxy's own address, such as 10.0.0.5/32, or a network that holds only proxies, since every peer in them can set the client address.",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("aether-edge serve -h lacks %q:\n%s", want, out)
		}
	}
}

// TestServeDeletesNothingBeforeItServes starts serve with a retired pair
// whose start fails, in option parsing and at the bind: the device token
// issued at the retired sign-in origin stays.
func TestServeDeletesNothingBeforeItServes(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	secret := filepath.Join(dir, "github-client-secret")
	if err := os.WriteFile(secret, []byte(fakeSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	svc, err := edge.New(edge.Config{DataDir: data, SigninOrigin: "http://localhost:8080", RelayOrigin: "http://127.0.0.1:8080",
		GitHub: &edge.OAuthApp{ClientID: "fake-client-id", ClientSecret: fakeSecret}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(data, "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close() //nolint:errcheck // test
	if _, err = db.Exec(`INSERT INTO accounts VALUES (1, 'acc', 'github', '1', 'owner@example.test', 'owner', 'Owner', 0, 0);
		INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin)
		VALUES ('dev', 'hash', 1, 'key', 'laptop', 0, 0, 'http://127.0.0.2:8080')`); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"AETHER_EDGE_DATA": data, "AETHER_EDGE_METRICS_LISTEN": "127.0.0.1:0",
		"AETHER_EDGE_SIGNIN_ORIGIN": "http://localhost:8080", "AETHER_EDGE_RELAY_ORIGIN": "http://127.0.0.1:8080",
		"AETHER_EDGE_GITHUB_CLIENT_ID": "fake-client-id", "AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE": secret,
	} {
		t.Setenv(k, v)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close() //nolint:errcheck // test
	for _, tt := range []struct {
		name, retired, want string
	}{
		{"port with a leading zero", "http://127.0.0.2:8080 http://127.0.0.3:08080 http://127.0.0.1:8080", "cannot be named in a signed notice"},
		{"listener in use", "http://127.0.0.2:8080 http://127.0.0.3:8080 http://127.0.0.1:8080", "development listener"},
	} {
		err := serve([]string{"--dev-listen", busy.Addr().String(), "--retired-pairs", tt.retired})
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v, want %q", tt.name, err, tt.want)
		}
		var n int
		if err = db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: %d devices, %v; a start that failed deleted the device token", tt.name, n, err)
		}
	}
}

// TestRetiredOptionsStar sends OPTIONS * to the server webServer builds
// for an edge with a retired pair: a retired host answers 410, a served
// one 200 as net/http does.
func TestRetiredOptionsStar(t *testing.T) {
	o, err := parseOptions([]string{"--dev-listen", "127.0.0.1:0", "--signin-origin", "http://localhost:8080", "--relay-origin", "http://127.0.0.1:8080",
		"--github-client-id", "fake-client-id", "--retired-pairs", "http://127.0.0.2:8080 http://127.0.0.3:8080 http://127.0.0.1:8080"},
		envOf(map[string]string{"AETHER_EDGE_GITHUB_CLIENT_SECRET": fakeSecret}))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := edge.New(edge.Config{DataDir: t.TempDir(), SigninOrigin: o.signinOrigin, RelayOrigin: o.relayOrigin, GitHub: o.github, Retired: o.retired})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	rl, err := relay.New(t.Context(), svc.RelayConfig(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rl.Shutdown(context.Background()) })
	svc.SetLink(rl)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	web := webServer(o, svc.Handler())
	go func() { _ = web.Serve(ln) }()
	t.Cleanup(func() { _ = web.Close() })
	for host, want := range map[string]string{"127.0.0.2:8080": "HTTP/1.1 410 Gone", "127.0.0.3:8080": "HTTP/1.1 410 Gone",
		"localhost:8080": "HTTP/1.1 200 OK", "127.0.0.1:8080": "HTTP/1.1 200 OK"} {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "OPTIONS * HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		_ = conn.Close()
		if got := resp.Proto + " " + resp.Status; got != want {
			t.Errorf("OPTIONS * on %s: %s, want %s", host, got, want)
		}
	}
}
