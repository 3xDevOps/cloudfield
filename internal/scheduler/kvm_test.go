//go:build kvm

package scheduler

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/runtime/vm"
	"github.com/3xDevOps/cloudfield/internal/vmimage"
)

// The session's memory limit, and what the mock agent holds of it: the
// largest process in the session, as a real agent often is.
const (
	kvmSessionMemory = 1 << 30
	kvmAgentMiB      = 200
)

// kvmEnhancedSession launches an Enhanced session of the mock agent in a
// real VM, as the server runs one, and returns once its task's turn ended.
// It needs what the VM runtime's own KVM tests need, and Docker.
func kvmEnhancedSession(t *testing.T) (*testEnv, *domain.Run, runtime.ID) {
	t.Helper()
	dir, kernel, server := os.Getenv("AETHER_VM_TEST_DIR"), os.Getenv("AETHER_VM_TEST_KERNEL"), os.Getenv("AETHER_VM_TEST_SERVER")
	if dir == "" || kernel == "" || server == "" {
		t.Skip("set AETHER_VM_TEST_DIR, AETHER_VM_TEST_KERNEL and AETHER_VM_TEST_SERVER to boot real VMs")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil || os.Geteuid() != 0 {
		t.Skip("needs root and /dev/kvm")
	}
	docker, err := runtime.NewDocker(runtime.WithLabels(map[string]string{"aether.test": t.Name()}))
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	images, err := vmimage.Open(filepath.Join(dir, "vm", "images"), kernel, docker, -1)
	if err != nil {
		t.Fatal(err)
	}
	vms, err := vm.New(vm.Config{
		Docker: docker, DataDir: dir, Images: images,
		Monitor: []string{server, vm.MonitorCommand}, Helper: server, ProgramUnit: memberhome.ProgramUnit,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(t.TempDir(), "acp-mock")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", agent, "./internal/acphost/acpmock/agent")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build the mock agent: %v\n%s", buildErr, out)
	}
	e := newTestEnv(t, func(cfg *Config) {
		cfg.Runtime = vms
		cfg.WorktreeMount = "/workspace"
		cfg.ServerBinary = server
		cfg.StandardImage = "docker.io/library/ubuntu:24.04"
		cfg.RunCPULimit, cfg.RunMemoryBytes = 1, kvmSessionMemory
		// Claude Code's profile, so the session is set up as one of its
		// sessions is, with the mock as its adapter.
		cfg.Harnesses = map[string]HarnessSpec{
			"claude": {ACPArgs: []string{"sh", "-c", `ACP_MOCK_BALLAST_MIB=` + strconv.Itoa(kvmAgentMiB) + ` exec "$HOME/.local/bin/acp-mock"`}},
		}
	})
	e.sched.UseCoordination(&fakeCoordinator{root: t.TempDir()}, t.TempDir(), false)
	home, err := e.cfg.Homes.Path(e.member.ID)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(agent)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(home, ".local/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, ".local/bin/acp-mock"), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	run, err := e.sched.Launch(ctx, e.ws.ID, e.member.ID, e.member.ID, "say pong", "claude", domain.LaunchACP)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	sc, err := e.sched.readSidecar(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	cid := runtime.ID(sc.ContainerID)
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_ = vms.Destroy(cctx, cid)
	})
	waitItems(t, e.sched, run.ID, "the task's turn", turnEnded("end_turn", 1))
	return e, run, cid
}

// TestKVMEnhancedAgentKilledMidSessionRestarts kills the agent of a live
// Enhanced session in a real VM, as the kernel's OOM killer or a stray
// `pkill` does, and sends the next prompt without pressing anything: the
// agent is back with its conversation and answers it.
func TestKVMEnhancedAgentKilledMidSessionRestarts(t *testing.T) {
	e, run, cid := kvmEnhancedSession(t)
	conversation := e.sched.acp.session(run.ID).SessionID()
	killed := time.Now()
	const kill = `missed=1; for p in /proc/[0-9]*; do if [ "$(cat "$p/comm" 2>/dev/null)" = acp-mock ]; then kill -9 "${p#/proc/}" && missed=0; fi; done; exit $missed`
	if code, _, stderr, err := e.cfg.Runtime.Exec(t.Context(), cid, []string{"/bin/sh", "-c", kill}, ""); err != nil || code != 0 {
		t.Fatalf("kill the agent: exit %d, %v: %s", code, err, stderr)
	}
	for deadline := killed.Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
		page, err := e.sched.ACPHistory(run.ID, 0, 0)
		if sess := e.sched.acp.session(run.ID); err == nil && hasNotice("Agent killed")(page.Items) && sess != nil {
			if got := sess.SessionID(); got != conversation {
				t.Fatalf("the restarted agent runs conversation %s, not the killed agent's %s", got, conversation)
			}
			break
		}
		if time.Now().After(deadline) {
			row, getErr := e.db.GetRun(t.Context(), run.ID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			t.Fatalf("a minute after its agent was killed the session is %s (%s) and waits for a person", row.Status, row.Reason)
		}
	}
	restarted := time.Since(killed)
	if _, err := e.sched.Inject(t.Context(), run.ID, e.member.ID, domain.AgentPrompt{Text: "after the kill"}, false, nil); err != nil {
		t.Fatalf("the prompt after the kill: %v", err)
	}
	waitItems(t, e.sched, run.ID, "the turn after the kill", turnEnded("end_turn", 2))
	t.Logf("agent killed, restarted with its conversation in %s, next prompt answered %s after the kill", restarted.Round(time.Millisecond), time.Since(killed).Round(time.Millisecond))
}

// kvmTurn sends prompt and returns what the agent said in the turn it
// starts, the nth of the session, with whether that turn ended: it does not
// when the agent died under it.
func kvmTurn(t *testing.T, e *testEnv, run domain.RunID, n int, prompt string) (said string, ended bool) {
	t.Helper()
	sess := e.sched.acp.session(run)
	before, err := e.sched.ACPHistory(run, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.sched.Inject(t.Context(), run, e.member.ID, domain.AgentPrompt{Text: prompt}, false, nil); err != nil {
		t.Fatalf("prompt %q: %v", prompt, err)
	}
	for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(100 * time.Millisecond) {
		page, err := e.sched.ACPHistory(run, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		ended = turnEnded("end_turn", n)(page.Items)
		if ended || e.sched.acp.session(run) != sess {
			var text strings.Builder
			for _, it := range page.Items[len(before.Items):] {
				if it.Kind == acphost.KindMessage && it.Message.Role == "assistant" {
					text.WriteString(it.Message.Text)
				}
			}
			return text.String(), ended
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt %q: the turn neither ended nor lost its agent in 4 minutes", prompt)
		}
	}
}

// TestKVMEnhancedAgentOutlivesCommandsThatFillTheSessionsMemory has the
// agent start 48 commands of some 30 MiB each, every one smaller than the
// agent and together more than the session's memory and swap: the kernel
// kills commands, the agent keeps its process and answers the next prompt.
// It then checks what the command wrapper hands a command.
func TestKVMEnhancedAgentOutlivesCommandsThatFillTheSessionsMemory(t *testing.T) {
	e, run, cid := kvmEnhancedSession(t)
	sess := e.sched.acp.session(run.ID)
	// A command holds its memory in its shell for 20 seconds, then prints
	// a line of its own; bash reports a killed one on a longer line.
	const fill = `exec bash -c 'mkfifo /tmp/hold; exec 3<>/tmp/hold; for i in $(seq 48); do (x=$(head -c 30000000 /dev/zero | tr "\0" x); read -t 20 -u 3 _; echo held) & done; wait'`
	said, ended := kvmTurn(t, e, run.ID, 2, acpmock.PromptRun+fill)
	events, _, err := e.cfg.Runtime.(runtime.MemoryEventsRuntime).MemoryEvents(t.Context(), cid)
	if err != nil {
		t.Fatal(err)
	}
	held := strings.Count("\n"+said, "\nheld\n")
	t.Logf("48 commands filled the session's %d MiB: the kernel killed %d processes, %d commands lived, the agent's turn ended: %t", kvmSessionMemory>>20, events.OOMKills, held, ended)
	if !ended || e.sched.acp.session(run.ID) != sess {
		t.Fatalf("the kernel killed the agent, the largest process at %d MiB, not one of its commands", kvmAgentMiB)
	}
	if events.OOMKills == 0 {
		t.Fatal("the commands did not fill the session's memory")
	}
	if said, ended = kvmTurn(t, e, run.ID, 3, "after the kills"); !ended {
		t.Fatalf("the agent did not answer the prompt after the kills: %q", said)
	}

	const probe = `cat /proc/self/oom_score_adj /proc/$PPID/oom_score_adj; echo "${AETHER_ACP_COMMAND_PREFIX_ENV-unset}"; printf '%s|' "a b" 'c"d' "$((1+2))"; exit 7`
	said, _ = kvmTurn(t, e, run.ID, 4, acpmock.PromptRun+probe)
	if want := "0\n-500\nunset\na b|c\"d|3|\nexit status 7"; said != want {
		t.Fatalf("a command through the wrapper printed %q, want %q: its own OOM score, the agent's, no request of the keeper's, its arguments and its exit status", said, want)
	}
}
