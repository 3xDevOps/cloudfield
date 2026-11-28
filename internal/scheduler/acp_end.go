package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/acpkeeper"
	"github.com/3xDevOps/cloudfield/internal/agentstatus"
	"github.com/3xDevOps/cloudfield/internal/domain"
)

// reconnected clears the disconnect the run's previous session reported, so
// the run is not shown disconnected until the reopened session's first turn
// state.
func (d *acpDriver) reconnected(entry *supervised) {
	d.s.mu.Lock()
	ended := entry.agentReport.State == agentstatus.Idle && disconnectedReason(entry.agentReport.Reason)
	d.s.mu.Unlock()
	if ended {
		d.report(entry.runID, agentstatus.Report{State: agentstatus.Idle, Reason: agentstatus.ReasonIdle})
	}
}

// reopenLost restores the session the agent lost from the agent's own
// history, or stops it when the agent lost it again soon after, unless the
// run stopped or reopened its session meanwhile.
func (d *acpDriver) reopenLost(entry *supervised, lost *acpRun, cause error) {
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	sess := lost.session
	current := d.runs[entry.runID] == lost && !lost.stopping && sess != nil
	d.mu.Unlock()
	if !current {
		return
	}
	defer d.needGuest(entry)()
	d.s.mu.Lock()
	again := time.Since(entry.sessionLostAt) < acpLostAgain
	entry.sessionLostAt = time.Now()
	d.s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	if again {
		if err := d.stopAdapterLocked(ctx, entry.runID); err != nil {
			slog.Warn("scheduler: stop the ACP server that lost its session", "run", entry.runID, "error", err)
		}
		d.fail(entry, fmt.Errorf("the agent lost its conversation again within %d minutes: %w", int(acpLostAgain.Minutes()), cause))
		return
	}
	if err := sess.Notice("warning", "Agent conversation lost", "The agent no longer has this conversation. Cloudfield is restarting the agent and restoring the conversation from its history; send again any message that failed."); err != nil {
		slog.Warn("scheduler: record the lost session notice", "run", entry.runID, "error", err)
	}
	// Its keeper's agent lost the session: open must replace it even when
	// stopping it fails, never reattach.
	d.record(entry, func() { entry.agentKeeper = nil })
	if err := d.open(ctx, entry, openRestore); err != nil {
		d.fail(entry, err)
	}
}

// reauthDue reports whether an auth refusal should restart the agent, and
// whether a restart within acpLostAgain already failed to fix it. A login
// written since that restart is one the agent has not read, so it earns one
// more restart in the window; an agent that rewrites its login on every
// start gets no more than that. A run whose agent reported an outcome waits
// for a turn end, which the restored session's idle state would fake, so it
// is not restarted.
func (d *acpDriver) reauthDue(entry *supervised, stamp string) (due, tried bool) {
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	again := time.Since(entry.authLostAt) < acpLostAgain
	if again && (entry.authStamp == stamp || entry.authReread) {
		return false, true
	}
	if entry.reported != "" || (entry.idleReason != "" && !entry.idleShown) {
		return false, false
	}
	if !again {
		entry.authLostAt = time.Now()
	}
	entry.authStamp, entry.authReread = stamp, again
	return true, false
}

// reauth restarts an agent that refused a turn for want of a sign-in, so it
// reads the account's login again, restores its session and shows the
// refusal with signIn, how the account's owner signs in again, and
// fileProblem, what the login file lacks. The refused prompt is not sent
// again: its sender already has the refusal, and mail is offered again on
// its own.
func (d *acpDriver) reauth(entry *supervised, lost *acpRun, st acphost.TurnState, signIn, fileProblem string) {
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	sess := lost.session
	current := d.runs[entry.runID] == lost && !lost.stopping && sess != nil
	d.mu.Unlock()
	if !current {
		d.s.mu.Lock()
		entry.authLostAt = time.Time{}
		d.s.mu.Unlock()
		return
	}
	if sess.State().TurnInFlight {
		// A newer turn started after the refusal, so the agent is not
		// stopped under it; a refusal of that turn may restart it.
		d.s.mu.Lock()
		entry.authLostAt = time.Time{}
		d.s.mu.Unlock()
		return
	}
	title, detail := "Sign-in refused", "The agent refused a turn with \"Authentication required\"; "+signIn+"."
	if fileProblem != "" {
		title, detail = "Agent signed out", detail+" The login file reads: "+fileProblem+"."
	}
	if err := sess.Notice("warning", title, detail); err != nil {
		slog.Warn("scheduler: record the sign-in notice", "run", entry.runID, "error", err)
	}
	defer d.needGuest(entry)()
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	// The restored session's idle state must not hide the refusal: nothing
	// sends a refused task again.
	d.record(entry, func() {
		entry.agentKeeper = nil
		entry.authRefused = fmt.Errorf("%w; %s", st.Failed, signIn)
	})
	if err := d.open(ctx, entry, openRestore); err != nil {
		d.s.mu.Lock()
		entry.authRefused = nil
		d.s.mu.Unlock()
		if acphost.AuthRequired(err) {
			err = fmt.Errorf("%w; %s", err, d.s.loginRefusedAdvice(d.s.runLogin(ctx, entry.runID), st.Failed, refusedAfterRestart))
		}
		d.fail(entry, err)
	}
}

// restoreHome runs before an agent starts in a live container, which the
// launch's ownership pass never revisits: a root command in another of the
// member's sessions can have taken the home since, leaving the agent unable
// to read its login or even run.
func (d *acpDriver) restoreHome(ctx context.Context, entry *supervised) error {
	was, err := d.s.restoreHomeRoot(ctx, entry)
	if err != nil {
		return fmt.Errorf("the agent cannot read its home: %w", err)
	}
	if was == "" {
		return nil
	}
	d.s.mu.Lock()
	member, user := entry.memberID, entry.runUser
	d.s.mu.Unlock()
	slog.Warn("scheduler: restored a member home a container had taken", "run", entry.runID, "member", member, "was", was, "now", user)
	d.notice(entry.runID, "warning", "Home restored", fmt.Sprintf("The home this session shares with your other sessions was owned by %s (uid:gid mode), so no agent on it could read its login or run. A root command in one of your sessions changed it; a package that runs systemd-tmpfiles does. Cloudfield gave it back to %s before starting the agent.", was, user))
	return nil
}

func (d *acpDriver) watch(entry *supervised, r *acpRun) {
	<-r.session.Done()
	d.mu.Lock()
	stopping := r.stopping
	if d.runs[entry.runID] == r {
		r.session = nil
	}
	d.mu.Unlock()
	if stopping {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace+10*time.Second)
	defer cancel()
	if r.keeper != nil && errors.Is(r.keeper.Err(), acpkeeper.ErrPreempted) {
		r.release()
		d.takenOver(entry, r)
		return
	}
	var cause error
	var killedBy int
	switch exit := r.exit(); {
	case exit != nil:
		cause = fmt.Errorf("the agent's ACP server exited with code %d", exit.Code)
		// The keeper reports a death by signal as 128 plus the signal.
		if exit.Code > 128 && exit.Code < 128+64 {
			killedBy = exit.Code - 128
		}
		_, _ = r.stderr.Write([]byte(exit.Stderr))
		_, _ = r.exec.Wait(ctx)
	case r.keeper != nil:
		cause = fmt.Errorf("the agent's ACP server closed its output: %w", errors.Join(r.keeper.Err(), errors.New("the keeper did not report the agent's exit")))
		_, _ = r.exec.Stop(ctx, acpStopGrace)
	default:
		status, err := r.exec.Wait(ctx)
		cause = fmt.Errorf("the agent's ACP server exited with code %d", status.Code)
		if err != nil {
			cause = fmt.Errorf("the agent's ACP server closed its output: %w", err)
			_, _ = r.exec.Stop(ctx, acpStopGrace)
		}
	}
	r.release()
	_ = r.exec.Detach()
	d.forget(entry, r.exec)
	cause = adapterError(cause, r.stderr)
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	current := d.runs[entry.runID] == r && !r.stopping && !r.ended
	if current {
		r.err = cause
	}
	d.mu.Unlock()
	if !current {
		return
	}
	d.s.mu.Lock()
	stopped := entry.stopNoticeLocked()
	restart := killedBy != 0 && stopped == "" && entry.launchMode != domain.LaunchHeadless
	again := restart && time.Since(entry.sessionLostAt) < acpLostAgain
	if restart {
		entry.sessionLostAt = time.Now()
	}
	d.s.mu.Unlock()
	switch {
	case again:
		cause = fmt.Errorf("%w; it was killed within %d minutes of its last restart, so Cloudfield did not restart it again", cause, int(acpLostAgain.Minutes()))
	case restart:
		// An agent started on a host short of memory takes back what its
		// kill freed, and the next kill may land on another session.
		available, floor, err := d.s.hostMemory()
		if err != nil || available >= floor {
			d.restartKilled(entry, killedBy)
			return
		}
		cause = fmt.Errorf("%w; the host has %s of memory available, under the %s every launch keeps free, so Cloudfield did not restart it", cause, formatGiB(available), formatGiB(floor))
	}
	idle := agentstatus.Report{State: agentstatus.Idle, Reason: acpEndedReason + cause.Error()}
	if stopped != "" {
		d.notice(entry.runID, "info", "Enhanced agent stopped", stopped)
		idle.Reason = acpEndedReason + stopped
	} else {
		d.notice(entry.runID, "error", "Enhanced agent exited", cause.Error())
	}
	if d.oneShot(entry) {
		d.endOneShot(entry, false, idle)
		return
	}
	d.report(entry.runID, idle)
}

// restartKilled starts the agent a signal killed again and restores its
// conversation, under the run's op lock. watch's acpLostAgain guard keeps an
// agent that is killed on every start from restarting in a loop.
func (d *acpDriver) restartKilled(entry *supervised, sig int) {
	why := "The agent's ACP server was killed"
	// The kernel's OOM killer sends signal 9.
	if sig == 9 {
		d.s.mu.Lock()
		target := oomTarget{entry.containerID, entry.workspaceID, entry.runID}
		d.s.mu.Unlock()
		if limit, at := d.s.checkOOMKill(context.Background(), target); time.Since(at) < oomKillWindow {
			why = "This session ran out of memory"
			if limit > 0 {
				why += " at its " + formatGiB(limit) + " limit"
			}
			why += ", and the kernel killed the agent's ACP server"
		}
	}
	d.notice(entry.runID, "warning", "Agent killed", fmt.Sprintf("%s (signal %d). Cloudfield is restarting the agent and restoring the conversation from its history. A turn that was in flight shows Turn interrupted and was lost with its commands and background tasks: send that message again.", why, sig))
	defer d.needGuest(entry)()
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	how := openRestore
	// The restore is likeliest to fail right after a kill for memory, and a
	// new conversation would replace the one the run records.
	if run, err := d.s.cfg.Store.GetRun(ctx, entry.runID); err == nil && d.s.resumes(run.Harness) {
		how = openWake
	}
	if err := d.open(ctx, entry, how); err != nil {
		d.fail(entry, err)
	}
}

// takenOver restarts the agent of a run whose keeper another attacher took
// over. This server process never preempts its own attachment, and only one
// server process may run on its state, so the other attacher is not trusted
// with the session.
func (d *acpDriver) takenOver(entry *supervised, lost *acpRun) {
	defer d.lockOp(entry.runID)()
	d.mu.Lock()
	current := d.runs[entry.runID] == lost && !lost.stopping && !lost.ended
	d.mu.Unlock()
	if !current {
		return
	}
	slog.Warn("scheduler: another attacher took over the agent's keeper", "run", entry.runID)
	d.notice(entry.runID, "error", "Agent connection taken over", "Another process attached to the agent's keeper, so Cloudfield restarted the agent and restored its session.")
	// open stops the keeper the run still records as its exec.
	d.record(entry, func() { entry.agentKeeper = nil })
	defer d.needGuest(entry)()
	ctx, cancel := context.WithTimeout(context.Background(), acpConnectTimeout)
	defer cancel()
	if err := d.open(ctx, entry, openRestore); err != nil {
		d.fail(entry, err)
	}
}

// needGuest keeps the memory brake off entry's VM while the server restarts
// its agent, which gives up on a guest that does not answer in time.
func (d *acpDriver) needGuest(entry *supervised) func() {
	d.s.mu.Lock()
	cid := entry.containerID
	d.s.mu.Unlock()
	return d.s.needGuest(cid)
}

func adapterError(err error, stderr *tailBuffer) error {
	if tail := strings.TrimSpace(stderr.String()); tail != "" {
		return fmt.Errorf("%w; stderr: %s", err, tail)
	}
	return err
}

// stopAdapter also stops a keeper the run records but no live session
// holds, as after an open that failed before it reattached.
func (d *acpDriver) stopAdapter(ctx context.Context, run domain.RunID) error {
	defer d.lockOp(run)()
	err := d.stopAdapterLocked(ctx, run)
	d.s.mu.Lock()
	entry := d.s.runs[run]
	d.s.mu.Unlock()
	if err == nil && entry != nil {
		d.stopStaleExec(ctx, entry)
	}
	return err
}

// stopAdapterLocked keeps the adapter's exec identity in the sidecar when the
// adapter may still be running, so a later open stops it.
func (d *acpDriver) stopAdapterLocked(ctx context.Context, run domain.RunID) error {
	d.mu.Lock()
	r := d.runs[run]
	delete(d.runs, run)
	if r != nil {
		r.stopping = true
	}
	d.mu.Unlock()
	if r == nil || r.exec == nil {
		return nil
	}
	if r.session != nil {
		// Closing writes to the agent's stdin, which an agent that stopped
		// reading never takes; stopping the exec ends that write.
		go func() { _ = r.session.Close() }()
		select {
		case <-r.session.Done():
		case <-time.After(acpStopGrace):
		case <-ctx.Done():
		}
	}
	err := d.stopExec(r.exec)
	r.release()
	if err != nil {
		return err
	}
	d.s.mu.Lock()
	defer d.s.mu.Unlock()
	if entry := d.s.runs[run]; entry != nil && entry.agentExec != nil && entry.agentExec.ExecID == r.exec.Identity().ExecID {
		entry.agentExec, entry.agentKeeper = nil, nil
		if err := d.s.writeSidecar(entry.sidecar()); err != nil {
			slog.Warn("scheduler: persist stopped ACP server", "run", run, "error", err)
		}
	}
	return nil
}

// detach is shutdown for one run, before its VM is saved: the adapter and
// its keeper run on with their records kept, and the next open after the
// restore reattaches and adopts the session. An adapter without a keeper,
// whose stdio this server holds, cannot outlive that and is stopped.
func (d *acpDriver) detach(ctx context.Context, run domain.RunID) error {
	defer d.lockOp(run)()
	r := d.letGo(run)
	if r == nil {
		if d.session(run) != nil {
			return d.stopAdapterLocked(ctx, run)
		}
		return nil
	}
	err := r.keeper.Detach(ctx)
	select {
	case <-r.session.Done():
	case <-ctx.Done():
	}
	r.release()
	if err != nil {
		return fmt.Errorf("detach from the agent's keeper: %w", err)
	}
	return nil
}

// letGo is detach without the keeper's answer, for a guest that gives none:
// the session's connection may end from then on without anything recording
// that end, and the next open reattaches to the keeper. It returns the run
// to release once that connection is no longer needed, nil for one with no
// session under a keeper.
func (d *acpDriver) letGo(run domain.RunID) *acpRun {
	d.mu.Lock()
	r := d.runs[run]
	if r == nil || r.session == nil || r.keeper == nil {
		d.mu.Unlock()
		return nil
	}
	delete(d.runs, run)
	r.stopping = true
	d.mu.Unlock()
	r.session.Detach()
	return r
}

// shutdown detaches every session from its keeper without recording or
// reporting its end: the adapter, its turn and its requests run on, and the
// next server process reattaches and adopts them.
func (d *acpDriver) shutdown() {
	d.mu.Lock()
	d.closed = true
	type detaching struct {
		r    *acpRun
		sess *acphost.Session
	}
	var all []detaching
	for _, r := range d.runs {
		r.stopping = true
		if r.session != nil {
			all = append(all, detaching{r, r.session})
		}
	}
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), acpStopGrace)
	defer cancel()
	var wg sync.WaitGroup
	for _, x := range all {
		wg.Go(func() {
			if x.r.keeper == nil {
				// An adapter run without a keeper ends with this process.
				_ = x.sess.Close()
			} else {
				// First, so no prompt starts and nothing records the end;
				// a cancel or answer written from here on fails with
				// acpkeeper.ErrDetached, and the next server delivers it.
				x.sess.Detach()
				if err := x.r.keeper.Detach(ctx); err != nil {
					slog.Warn("scheduler: detach from the agent's keeper", "error", err)
				}
			}
			select {
			case <-x.sess.Done():
			case <-ctx.Done():
			}
			// Acks what the session handled before it ended.
			x.r.release()
		})
	}
	wg.Wait()
}
