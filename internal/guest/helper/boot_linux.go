package helper

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/guest"
	"github.com/3xDevOps/cloudfield/internal/guest/guestpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	dockerSocket = "/var/run/docker.sock"
	dockerdLog   = "/var/log/aether-dockerd.log"
)

// boot is Docker's start of a container: dockerd first, then the setup
// script, then the main process, each only when the one before succeeded.
// dockerd has only to be started, not to serve.
func (s *service) boot(req *guestpb.BootRequest) (*guestpb.BootReply, error) {
	main := processSpec(req.GetMain())
	main.ID = guest.MainID
	if err := checkSpec(main); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "main process: %v", err)
	}
	if s.cgroup != nil {
		if err := s.cgroup.setLimit(req.GetPidsLimit()); err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	// Docker creates a container's missing working directory.
	if main.Dir != "" {
		if err := os.MkdirAll(main.Dir, 0o755); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "working directory: %v", err)
		}
	}
	var dockerd *process
	if req.GetNestedDocker() {
		var err error
		if dockerd, err = s.startDockerd(main); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	if script := req.GetSetupScript(); script != "" {
		reply, err := s.setup(main, script)
		if err != nil || reply.GetSetupExitCode() != 0 {
			if dockerd != nil && reply != nil {
				select {
				case <-dockerd.done:
					reply.SetupOutput = fmt.Appendf(reply.SetupOutput, "\ndockerd exited %d: %s", dockerd.code, logTail(dockerdLog))
				default:
				}
			}
			return reply, err
		}
	}
	if _, err := s.procs.spawn(spawnSpec{ProcessSpec: main}); err != nil {
		return nil, rpcError(err)
	}
	return &guestpb.BootReply{}, nil
}

// setup runs the script with the main process's user, environment and
// directory; its stderr joins stdout so the end of the output is where it
// failed.
func (s *service) setup(main guest.ProcessSpec, script string) (*guestpb.BootReply, error) {
	spec := main
	spec.ID, spec.TTY, spec.Lossless = guest.SetupID, nil, false
	spec.Argv = []string{"/bin/sh", "-ec", "exec 2>&1; " + script}
	p, err := s.procs.spawn(spawnSpec{ProcessSpec: spec})
	if err != nil {
		var failed *execError
		if errors.As(err, &failed) {
			return &guestpb.BootReply{SetupExitCode: int32(failed.code), SetupOutput: []byte(failed.Error())}, nil
		}
		return nil, rpcError(err)
	}
	// Docker gives setup no input, so a prompt reads end of file instead of
	// holding Boot forever.
	if err := p.closeInput(); err != nil {
		return nil, rpcError(err)
	}
	<-p.done
	p.stdout.waitClosed()
	if p.code == 0 {
		return nil, nil
	}
	output, cut := p.stdout.last(guest.SetupOutputLimit)
	return &guestpb.BootReply{SetupExitCode: int32(p.code), SetupOutput: output, SetupOutputCut: cut}, nil
}

// startDockerd starts the image's dockerd when it has dockerd and docker,
// with the socket's group set to the main process's so its user reaches it,
// and nested containers under the session's process limit. It returns nil
// when the image has no dockerd.
func (s *service) startDockerd(main guest.ProcessSpec) (*process, error) {
	_, home, err := credential("0")
	if err != nil {
		return nil, err
	}
	// dockerd runs as root, so it is never looked up on the session's PATH,
	// which starts in the member's writable home.
	env := withDefaults(slices.DeleteFunc(slices.Clone(main.Env), func(e string) bool { return strings.HasPrefix(e, "PATH=") }), home, false)
	for _, name := range []string{"dockerd", "docker"} {
		if _, err = lookPath(name, defaultPath, "/"); err != nil {
			return nil, nil
		}
	}
	cred, _, err := credential(main.User)
	if err != nil {
		return nil, err
	}
	group := 0
	if cred != nil {
		group = int(cred.Gid)
	}
	// A saved environment can hold a stopped daemon's files.
	for _, stale := range []string{"/var/run/docker.pid", dockerSocket, "/var/run/docker/containerd/containerd.pid", "/var/run/docker/containerd/containerd.sock"} {
		_ = os.Remove(stale)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: dockerSocket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", dockerSocket, err)
	}
	// The socket file stays for dockerd once the helper's listener closes.
	listener.SetUnlinkOnClose(false)
	socket, err := listener.File()
	_ = listener.Close()
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", dockerSocket, err)
	}
	defer func() { _ = socket.Close() }()
	if err = errors.Join(os.Chown(dockerSocket, 0, group), os.Chmod(dockerSocket, 0o660)); err != nil {
		return nil, fmt.Errorf("give %s to group %d: %w", dockerSocket, group, err)
	}
	log, err := os.OpenFile(dockerdLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dockerdLog, err)
	}
	defer func() { _ = log.Close() }()
	if err = log.Chown(0, group); err != nil {
		return nil, fmt.Errorf("give %s to group %d: %w", dockerdLog, group, err)
	}
	// Socket activation passes the socket as fd 3 and names the pid it is
	// for, which only the shell that becomes dockerd knows.
	argv := []string{"/bin/sh", "-c", `LISTEN_FDS=1 LISTEN_PID=$$ exec "$@" 3<&5 5<&-`, "dockerd", "dockerd", "-H", "fd://"}
	if s.cgroup != nil {
		argv = append(argv, "--cgroup-parent", sessionCgroupParent)
	}
	p, err := s.procs.spawn(spawnSpec{ProcessSpec: guest.ProcessSpec{ID: dockerdID, Argv: argv, Env: env, NoNewPrivileges: main.NoNewPrivileges}, log: log, passed: socket})
	if err != nil {
		return nil, fmt.Errorf("start dockerd: %w", err)
	}
	go func() {
		<-p.done
		// The end of the session kills dockerd with everything else.
		if m, err := s.procs.get(guest.MainID); err == nil {
			select {
			case <-m.done:
				return
			default:
			}
		}
		slog.Error("guest: dockerd exited", "code", p.code, "log", logTail(dockerdLog))
	}()
	return p, nil
}

// logTail is the end of a log, where a failure is.
func logTail(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return err.Error()
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > 4<<10 {
		_, _ = f.Seek(-4<<10, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	return strings.TrimSpace(string(data))
}
