package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cgroupRoot = "/sys/fs/cgroup/fclazy"
	bootArgs   = "rootfstype=ext4 init=/fcguest console=ttyS0 loglevel=5 panic=-1 reboot=k page_reporting.page_reporting_order=0 i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd"
	chBootArgs = "root=/dev/vda ro rootfstype=ext4 init=/fcguest console=ttyS0 loglevel=5 panic=-1 page_reporting.page_reporting_order=0"
)

var (
	vmSeq      int
	consoleOff = map[string]int64{}
	badConsole = regexp.MustCompile(`(?i)BUG:|Oops|panic|Bad page|stall|lockup|hung_task|segfault|Call Trace|general protection|Out of memory|oom-kill`)
)

type vm struct {
	dir  string
	cmd  *exec.Cmd
	done chan error
	cg   string
	conn net.Conn
	rd   *bufio.Reader
}

func (m *vm) alive() bool { return m != nil && len(m.done) == 0 }

func (m *vm) kill() {
	if m == nil || m.cmd.Process == nil {
		return
	}
	if m.conn != nil {
		m.conn.Close()
	}
	_ = m.cmd.Process.Kill()
	<-m.done
	if m.cg != "" {
		_ = os.Remove(m.cg)
	}
}

func (m *vm) tail() string {
	var out []string
	for _, f := range []string{"console.log", "serial.log", "vmm.log"} {
		data, _ := os.ReadFile(filepath.Join(m.dir, f))
		if len(data) > 2500 {
			data = data[len(data)-2500:]
		}
		out = append(out, f+": "+strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", " | "))
	}
	return strings.Join(out, " || ")
}

// console returns what the VM's console and monitor logs gained since the
// last call for this directory, and how many of those lines look like trouble.
func console(dir string) (bad int, lines []string) {
	for _, name := range []string{"console.log", "serial.log", "vmm.log"} {
		path := filepath.Join(dir, name)
		data, _ := os.ReadFile(path)
		fresh := data[min(int64(len(data)), consoleOff[path]):]
		consoleOff[path] = int64(len(data))
		for _, l := range strings.Split(string(fresh), "\n") {
			if l = strings.TrimSpace(l); l == "" {
				continue
			}
			lines = append(lines, name+": "+l)
			if badConsole.MatchString(l) {
				bad++
			}
		}
	}
	return
}

// start runs a hypervisor in dir, in its own memory cgroup, with relative
// paths, so a snapshot restores in any directory holding the same file names.
func start(dir, bin string, args ...string) (*vm, time.Time, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	consoleLog, err := os.OpenFile(filepath.Join(dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer consoleLog.Close()
	vmmLog, err := os.OpenFile(filepath.Join(dir, "vmm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer vmmLog.Close()
	cmd.Stdout, cmd.Stderr = consoleLog, vmmLog
	m := &vm{dir: dir, cmd: cmd, done: make(chan error, 1)}
	vmSeq++
	cg := filepath.Join(cgroupRoot, fmt.Sprintf("vm%d", vmSeq))
	if err = os.Mkdir(cg, 0o755); err == nil {
		if fd, oerr := os.Open(cg); oerr == nil {
			defer fd.Close()
			cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(fd.Fd())}
			m.cg = cg
		}
	}
	t0 := time.Now()
	if err = cmd.Start(); err != nil {
		return nil, t0, err
	}
	go func() { m.done <- cmd.Wait() }()
	return m, t0, nil
}

func call(sock, method, url string, body any) (string, error) {
	client := &http.Client{Timeout: 20 * time.Minute, Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}}
	var payload io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, "http://localhost"+url, payload)
	if err != nil {
		return "", err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return string(data), fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, data)
	}
	return string(data), nil
}

func (m *vm) api(method, url string, body any) error {
	_, err := call(filepath.Join(m.dir, "api.sock"), method, url, body)
	return err
}

// dial connects to the guest's vsock port 5001 and gets a pong.
func (m *vm) dial(deadline time.Time) error {
	var err error
	for {
		if time.Now().After(deadline) || !m.alive() {
			return fmt.Errorf("guest never answered: %v", err)
		}
		var c net.Conn
		if c, err = net.Dial("unix", filepath.Join(m.dir, "v.sock")); err == nil {
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			rd := bufio.NewReader(c)
			var line string
			if _, err = io.WriteString(c, "CONNECT 5001\n"); err == nil {
				if line, err = rd.ReadString('\n'); err == nil && !strings.HasPrefix(line, "OK") {
					err = fmt.Errorf("vsock CONNECT answered %q", line)
				}
			}
			if err == nil {
				if _, err = io.WriteString(c, "ping\n"); err == nil {
					if line, err = rd.ReadString('\n'); err == nil && line != "pong\n" {
						err = fmt.Errorf("ping answered %q", line)
					}
				}
			}
			if err == nil {
				m.conn, m.rd = c, rd
				return nil
			}
			c.Close()
		}
		time.Sleep(time.Millisecond)
	}
}

// guest sends one command line and returns the answer line.
func (m *vm) guest(format string, args ...any) (string, error) {
	line := fmt.Sprintf(format, args...)
	_ = m.conn.SetDeadline(time.Now().Add(15 * time.Minute))
	if _, err := io.WriteString(m.conn, line+"\n"); err != nil {
		return "", fmt.Errorf("%s: %w", line, err)
	}
	out, err := m.rd.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%s: %w", line, err)
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "err") {
		return out, fmt.Errorf("%s: guest answered %q", line, out)
	}
	return out, nil
}

// field reads k=v from a guest answer.
func field(line, key string) string {
	for _, f := range strings.Fields(line) {
		if v, ok := strings.CutPrefix(f, key+"="); ok {
			return v
		}
	}
	return ""
}

func fieldInt(line, key string) int {
	n, _ := strconv.Atoi(field(line, key))
	return n
}

type vmConfig struct {
	hv    string // fc or ch
	mem   int
	track bool
}

// boot starts a fresh VM in dir, which holds rootfs.ext4 and data.raw, and
// waits for the guest's ready connection and a pong.
func boot(dir string, c vmConfig) (*vm, time.Duration, error) {
	for _, f := range []string{"api.sock", "v.sock", "v.sock_5000"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	l, err := net.Listen("unix", filepath.Join(dir, "v.sock_5000"))
	if err != nil {
		return nil, 0, err
	}
	defer l.Close()
	var bin string
	var args []string
	if c.hv == "ch" {
		bin = *chBin
		args = []string{
			"--api-socket", "path=api.sock",
			"--vsock", "cid=3,socket=v.sock",
			"--serial", "file=serial.log", "--console", "off",
			"--kernel", *kernel, "--cmdline", chBootArgs,
			"--disk", "path=rootfs.ext4,image_type=raw,backing_files=off,id=root,serial=root,readonly=on",
			"path=data.raw,image_type=raw,backing_files=off,id=data,serial=data",
			"--cpus", "boot=2,nested=off",
			"--memory", fmt.Sprintf("size=%dM,shared=off", c.mem),
			"--balloon", "size=0,free_page_reporting=on",
		}
	} else {
		bin = *fcBin
		cfg := map[string]any{
			"boot-source": map[string]any{"kernel_image_path": *kernel, "boot_args": bootArgs},
			"drives": []any{
				map[string]any{"drive_id": "rootfs", "path_on_host": "rootfs.ext4", "is_root_device": true, "is_read_only": true},
				map[string]any{"drive_id": "data", "path_on_host": "data.raw", "is_root_device": false, "is_read_only": false, "discard": true, "io_engine": "Sync"},
			},
			"machine-config":     map[string]any{"vcpu_count": 2, "mem_size_mib": c.mem, "smt": false, "track_dirty_pages": c.track},
			"vsock":              map[string]any{"guest_cid": 3, "uds_path": "v.sock"},
			"balloon":            map[string]any{"amount_mib": 0, "deflate_on_oom": false, "free_page_reporting": true},
			"network-interfaces": []any{map[string]any{"iface_id": "eth0", "host_dev_name": *tap, "guest_mac": "06:00:AC:1F:4D:02"}},
		}
		data, _ := json.Marshal(cfg)
		if err = os.WriteFile(filepath.Join(dir, "cfg.json"), data, 0o644); err != nil {
			return nil, 0, err
		}
		args = []string{"--config-file", "cfg.json", "--enable-pci", "--api-sock", "api.sock"}
	}
	m, t0, err := start(dir, bin, args...)
	if err != nil {
		return nil, 0, err
	}
	got := make(chan error, 1)
	go func() {
		c, aerr := l.Accept()
		if aerr == nil {
			c.Close()
		}
		got <- aerr
	}()
	select {
	case err = <-got:
	case werr := <-m.done:
		m.done <- werr
		err = fmt.Errorf("hypervisor exited before ready: %v", werr)
	case <-time.After(90 * time.Second):
		err = errors.New("no ready in 90s")
	}
	ready := time.Since(t0)
	if err == nil {
		err = m.dial(time.Now().Add(20 * time.Second))
	}
	if err != nil {
		tail := m.tail()
		m.kill()
		return nil, 0, fmt.Errorf("boot: %w; %s", err, tail)
	}
	return m, ready, nil
}

type wakeTimes struct {
	apiUp, resumed, pong, exec time.Duration // since the hypervisor's exec
}

// wake restores a saved VM in dir. mem and state are the memory file and the
// state file (fc) or mem is the snapshot directory (ch).
func wake(dir string, c vmConfig, mem, state string) (*vm, wakeTimes, error) {
	var w wakeTimes
	for _, f := range []string{"api.sock", "v.sock"} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	deadline := time.Now().Add(120 * time.Second)
	var m *vm
	var t0 time.Time
	var err error
	fail := func(err error) (*vm, wakeTimes, error) {
		tail := m.tail()
		m.kill()
		return nil, w, fmt.Errorf("wake: %w; %s", err, tail)
	}
	if c.hv == "ch" {
		if m, t0, err = start(dir, *chBin, "--api-socket", "path=api.sock", "--restore", "source_url=file://"+mem+",memory_restore_mode=copyonwrite"); err != nil {
			return nil, w, err
		}
		state := ""
		for state != "Paused" && state != "Running" {
			if time.Now().After(deadline) || !m.alive() {
				return fail(errors.New("never reached Paused"))
			}
			time.Sleep(time.Millisecond)
			out, cerr := call(filepath.Join(dir, "api.sock"), http.MethodGet, "/api/v1/vm.info", nil)
			if cerr == nil {
				var info struct {
					State string `json:"state"`
				}
				_ = json.Unmarshal([]byte(out), &info)
				state = info.State
			}
		}
		w.apiUp = time.Since(t0)
		if state == "Paused" {
			if err = m.api(http.MethodPut, "/api/v1/vm.resume", nil); err != nil {
				return fail(err)
			}
		}
	} else {
		if m, t0, err = start(dir, *fcBin, "--api-sock", "api.sock", "--enable-pci"); err != nil {
			return nil, w, err
		}
		for {
			if c, derr := net.Dial("unix", filepath.Join(dir, "api.sock")); derr == nil {
				c.Close()
				break
			}
			if time.Now().After(deadline) || !m.alive() {
				return fail(errors.New("no API socket"))
			}
			time.Sleep(500 * time.Microsecond)
		}
		w.apiUp = time.Since(t0)
		if err = m.api(http.MethodPut, "/snapshot/load", map[string]any{
			"snapshot_path":     state,
			"mem_backend":       map[string]string{"backend_type": "File", "backend_path": mem},
			"track_dirty_pages": c.track,
			"resume_vm":         true,
		}); err != nil {
			return fail(err)
		}
	}
	w.resumed = time.Since(t0)
	if err = m.dial(deadline); err != nil {
		return fail(err)
	}
	w.pong = time.Since(t0)
	if _, err = m.guest("exec"); err != nil {
		return fail(err)
	}
	w.exec = time.Since(t0)
	return m, w, nil
}

// sleep pauses the VM and saves it: a Diff snapshot to diff/diffState and,
// when full is set, a Full snapshot of the same paused VM to full.
func (m *vm) sleep(diff, diffState, full string) (paused, diffTook, fullTook time.Duration, err error) {
	m.conn.Close()
	t := time.Now()
	if err = m.api(http.MethodPatch, "/vm", map[string]string{"state": "Paused"}); err != nil {
		return
	}
	paused = time.Since(t)
	_ = os.Remove(diff)
	t = time.Now()
	if err = m.api(http.MethodPut, "/snapshot/create", map[string]string{"snapshot_type": "Diff", "snapshot_path": diffState, "mem_file_path": diff}); err != nil {
		return
	}
	diffTook = time.Since(t)
	if full != "" {
		_ = os.Remove(full)
		t = time.Now()
		err = m.api(http.MethodPut, "/snapshot/create", map[string]string{"snapshot_type": "Full", "snapshot_path": full + ".state", "mem_file_path": full})
		fullTook = time.Since(t)
	}
	return
}

type memStat struct {
	vmas                                                     int
	anon, file, fileMapped, shmem, current, rssAnon, rssFile int // MiB
}

func (s memStat) String() string {
	return fmt.Sprintf("vmas=%d cg_anon_mib=%d cg_file_mib=%d cg_file_mapped_mib=%d cg_shmem_mib=%d cg_current_mib=%d rss_anon_mib=%d rss_file_mib=%d",
		s.vmas, s.anon, s.file, s.fileMapped, s.shmem, s.current, s.rssAnon, s.rssFile)
}

func (m *vm) stat() memStat {
	var s memStat
	pid := m.cmd.Process.Pid
	maps, _ := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	s.vmas = bytes.Count(maps, []byte("\n"))
	status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, l := range strings.Split(string(status), "\n") {
		if f := strings.Fields(l); len(f) >= 2 {
			kb, _ := strconv.Atoi(f[1])
			switch f[0] {
			case "RssAnon:":
				s.rssAnon = kb >> 10
			case "RssFile:":
				s.rssFile = kb >> 10
			}
		}
	}
	if m.cg != "" {
		data, _ := os.ReadFile(filepath.Join(m.cg, "memory.stat"))
		for _, l := range strings.Split(string(data), "\n") {
			if f := strings.Fields(l); len(f) == 2 {
				b, _ := strconv.Atoi(f[1])
				switch f[0] {
				case "anon":
					s.anon = b >> 20
				case "file":
					s.file = b >> 20
				case "file_mapped":
					s.fileMapped = b >> 20
				case "shmem":
					s.shmem = b >> 20
				}
			}
		}
		data, _ = os.ReadFile(filepath.Join(m.cg, "memory.current"))
		b, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		s.current = b >> 20
	}
	return s
}
