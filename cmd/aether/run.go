package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/shellquote"
)

func init() {
	register(command{
		name:  "session",
		short: "launch an agent session",
		usage: "cloudfield session <task> [flags]\n" +
			"cloudfield session - [flags]\n" +
			"cloudfield session --template <name> [--param <name=value>]... [--workspace <name>]\n" +
			"cloudfield session switch <session-id> --mode standard|enhanced",
		example: "cloudfield session \"fix the flaky login test\" --agent claude\n" +
			"cloudfield session \"review this dependency upgrade\" --agent claude --permission isolated\n" +
			"cloudfield session - --agent codex --idempotency-key fix-login-1 < task.md\n" +
			"cloudfield session --template nightly-audit\n" +
			"cloudfield session switch run-d6ay38tj8j --mode enhanced",
		flags: true,
		run:   runRun,
	})
}

func runRun(args []string) error {
	if len(args) > 1 && args[0] == "switch" && !strings.HasPrefix(args[1], "-") {
		return runSwitch(args[1:])
	}
	fs := newFlagSet("session")
	agent := fs.String("agent", "", "agent name")
	mode := fs.String("mode", "", launchModeHelp+" (default: "+defaultModeHelp+")")
	model := fs.String("model", "", modelHelp)
	effort := fs.String("effort", "", effortHelp)
	permission := fs.String("permission", "", "root, default or isolated: root lets sudo run without asking, default asks you before each sudo, isolated keeps every change outside the checkout to the session (default: default)")
	workspace := fs.String("workspace", "", "workspace ID or name (default: the only workspace)")
	account := fs.String("account", "", "member ID whose shared agent account to use")
	template := fs.String("template", "", "launch a saved task template instead of a prompt")
	cachedBase := fs.String("cached-base", "", "retry a launch from an exact accepted base commit")
	fallbackOrder := fs.String("fallback", "", fallbackHelp)
	noFallback := fs.Bool("no-fallback", false, noFallbackHelp)
	children := fs.String("children", "", "subsessions this session's agent may start: on or off for this session only (default: your setting, cloudfield subsessions)")
	idempotencyKey := fs.String("idempotency-key", "", "the key a failed or interrupted launch printed: the retry returns that launch's session instead of starting another (default: a new key)")
	noWait := fs.Bool("no-wait", false, "fail at once when the server has no memory or disk for the session, instead of waiting until it has")
	linearIssue := fs.String("linear-issue", "", "link the session to a Linear issue, https://linear.app/<workspace>/issue/<TEAM>-<number> (default: the first Linear issue link in the task)")
	var repos stringList
	fs.Var(&repos, "repo", "also check out this workspace ID or name at /repos/<name> with its own session branch (repeatable)")
	params := kvFlag{}
	fs.Var(params, "param", "value for a template parameter, name=value (repeatable)")

	// A template launch carries no prompt, so the leading positional
	// argument is optional here.
	task := ""
	if len(args) > 0 && args[0] != "--" && (args[0] == "-" || !strings.HasPrefix(args[0], "-")) {
		task, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if task == "" && fs.NArg() == 1 {
		task = fs.Arg(0)
	}
	if *template != "" {
		if task != "" || *agent != "" || *model != "" || *effort != "" || *permission != "" || *account != "" || *cachedBase != "" || *fallbackOrder != "" || *noFallback || len(repos) > 0 || *idempotencyKey != "" || *children != "" || *linearIssue != "" {
			return fmt.Errorf("--template launches a saved definition: drop the task prompt, --agent, --model, --effort, --permission, --account, --cached-base, --fallback, --no-fallback, --repo, --children, --linear-issue, and --idempotency-key")
		}
		return launchTemplate(*workspace, *template, params)
	}
	if *mode != "" {
		wireMode, err := parseLaunchMode(*mode)
		if err != nil {
			return err
		}
		*mode = wireMode
	}
	fallbacks, err := parseFallback(*fallbackOrder, *noFallback)
	if err != nil {
		return err
	}
	if *children != "" && *children != "on" && *children != "off" {
		return fmt.Errorf("invalid --children %q: want on or off", *children)
	}
	if *agent == "" || fs.NArg() > 1 || (task == "" && *mode == "headless") {
		return fmt.Errorf("usage: cloudfield session [\"task\"|-] --agent <name> [--mode standard|enhanced|background] [--model <model>] [--effort <level>] [--permission root|default|isolated] [--workspace] [--account <member-id>] [--fallback <member-id>,... | --no-fallback] [--cached-base <full-sha>] [--repo <workspace> ...] [--children on|off] [--linear-issue <url>] [--idempotency-key <key>]\n   (a task is required with --mode background; - reads it from stdin)\n   or: cloudfield session --template <name> [--param k=v] [--workspace]\n   or: cloudfield session switch <session-id> --mode standard|enhanced")
	}
	fromStdin := task == "-"
	task, err = stdinText(task, "task", os.Stdin)
	if err != nil {
		return err
	}
	return withControl(func(c *protocol.Client) error {
		var list protocol.WorkspaceListResult
		if err := c.Call(protocol.MethodWorkspaceList, struct{}{}, &list); err != nil {
			return err
		}
		wsID, err := pickWorkspace(list.Workspaces, *workspace)
		if err != nil {
			return err
		}
		// Only an option that needs a feature needs the answer: a plain
		// launch goes ahead, without waiting, when it cannot be read.
		var info protocol.ServerInfoResult
		if err := c.Call(protocol.MethodServerInfo, struct{}{}, &info); err != nil && (len(repos) > 0 || *permission != "") {
			return err
		}
		if len(repos) > 0 && !slices.Contains(info.Features, protocol.FeatureMultiRepo) {
			return fmt.Errorf("--repo: server %s does not support sessions with several repositories; update the server with aether server update", info.ServerVersion)
		}
		if *permission != "" && !slices.Contains(info.Features, protocol.FeaturePermission) {
			return fmt.Errorf("--permission: server %s runs every agent as root and has no permission tiers; update the server with aether server update", info.ServerVersion)
		}
		params := launchParams(wsID, task, *agent, *mode, *model, *effort, *account, *cachedBase)
		params.FallbackAccountMemberIDs, params.NoFallback = fallbacks, *noFallback
		params.Children = *children
		params.Permission = *permission
		params.LinearIssueURL = *linearIssue
		params.IdempotencyKey = cmp.Or(*idempotencyKey, cli.NewControlSessionID())
		for _, repo := range repos {
			id, err := workspaceIDIn(list.Workspaces, repo)
			if err != nil {
				return fmt.Errorf("--repo %s: %w", repo, err)
			}
			params.ExtraWorkspaceIDs = append(params.ExtraWorkspaceIDs, id)
		}
		// A server that keeps a launch it has no room for is asked to,
		// unless --no-wait.
		params.Wait = !*noWait && slices.Contains(info.Features, protocol.FeatureLaunchWait)
		res, failed := launchWaiting(c, params, os.Stderr, waitingLaunchEvery, launchCommand(params, fromStdin))
		if failed != nil {
			return launchError(failed, params, fromStdin)
		}
		if res.Replayed {
			_, _ = fmt.Fprintf(os.Stderr, "idempotency key %s already launched session %s; nothing new was launched\n", params.IdempotencyKey, res.Run.ID)
		}
		fmt.Printf("session %s %s\n", res.Run.ID, res.Run.Status)
		return nil
	})
}

// waitingLaunchEvery is how often a launch the server keeps for want of
// room is asked after, and cancelWaitFor how long an interrupted command
// waits for the server to cancel it.
const (
	waitingLaunchEvery = 5 * time.Second
	cancelWaitFor      = 10 * time.Second
)

// errWaitEnded ends a command whose launch waited for room and did not
// become a running session: cancelled by an interrupt, left waiting, or
// started and failed. Its text is the whole message.
type errWaitEnded struct {
	text string
	code int
}

func (e *errWaitEnded) Error() string { return e.text }

// launchWaiting launches params. While the server keeps the launch because
// it has no room, it says why on w, each time the server's refusal changes,
// and asks again every interval until the session has started or the wait
// ends. An interrupt cancels a waiting launch: nobody watches for a session
// that would start hours later. again is the command that follows the same
// launch, for when this one cannot.
func launchWaiting(c *protocol.Client, params protocol.RunLaunchParams, w io.Writer, interval time.Duration, again string) (protocol.RunLaunchResult, error) {
	interrupt := make(chan os.Signal, 2)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	waiting, said := "", ""
	// left is how a command that stops watching says where the launch is.
	left := func(why string, code int) error {
		return &errWaitEnded{code: code, text: fmt.Sprintf("%s\nthe launch may still be waiting on the server and start later. To follow it, or cancel it with Ctrl-C, run:\n  %s", why, again)}
	}
	cancel := func() error {
		var ended protocol.LaunchWaiting
		done := make(chan error, 1)
		// The call in flight, if any, holds the connection until it returns.
		go func() {
			done <- c.Call(protocol.MethodLaunchWaitingCancel, protocol.LaunchWaitingCancelParams{ID: waiting}, &ended)
		}()
		select {
		case err := <-done:
			switch {
			case err != nil:
				return &errWaitEnded{code: 130, text: fmt.Sprintf("interrupted; the waiting launch %s was not cancelled: %v", waiting, err)}
			case ended.State != "cancelled":
				return &errWaitEnded{code: 130, text: fmt.Sprintf("interrupted; the launch %s had already ended %s: %s", waiting, ended.State, ended.Refusal)}
			}
			return &errWaitEnded{code: 130, text: fmt.Sprintf("interrupted; the launch %s was waiting for room and is cancelled: nothing started", waiting)}
		case <-interrupt:
		case <-time.After(cancelWaitFor):
		}
		return left("interrupted; the server did not confirm the cancel", 130)
	}
	for {
		var res protocol.RunLaunchResult
		done := make(chan error, 1)
		go func() { done <- c.Call(protocol.MethodRunLaunch, params, &res) }()
		select {
		case err := <-done:
			if err != nil && waiting != "" {
				var rpcErr *protocol.Error
				if errors.As(err, &rpcErr) && (rpcErr.Code == protocol.CodeUnavailable || rpcErr.Code == protocol.CodeConflict) && strings.Contains(rpcErr.Message, "waited for room and ended") {
					// The wait is over without a session: the server says how.
					return protocol.RunLaunchResult{}, &errWaitEnded{code: 1, text: rpcErr.Message}
				}
				return protocol.RunLaunchResult{}, left(err.Error(), 1)
			}
			if err != nil {
				return protocol.RunLaunchResult{}, err
			}
		case <-interrupt:
			if waiting != "" {
				return protocol.RunLaunchResult{}, cancel()
			}
			if !params.Wait {
				return protocol.RunLaunchResult{}, errInterrupted
			}
			// The server may be keeping this launch already: only its
			// answer names what to cancel.
			select {
			case err := <-done:
				if err != nil {
					return protocol.RunLaunchResult{}, errInterrupted
				}
				if res.Waiting == nil {
					return res, nil
				}
				waiting = res.Waiting.ID
				return protocol.RunLaunchResult{}, cancel()
			case <-interrupt:
			case <-time.After(cancelWaitFor):
			}
			return protocol.RunLaunchResult{}, left("interrupted before the server answered", 130)
		}
		switch {
		case res.Waiting != nil:
			waiting = res.Waiting.ID
			// The figures in a refusal differ at every try; its kind rarely does.
			if kind := strings.Map(dropDigits, res.Waiting.Refusal); kind != said {
				said = kind
				_, _ = fmt.Fprintf(w, "waiting for room on the server: launch %s, until %s (Ctrl-C cancels it; --no-wait fails at once): %s\n", waiting, res.Waiting.ExpiresAt, res.Waiting.Refusal)
			}
		case waiting == "":
			return res, nil
		// A launch that waited is answered as soon as its session exists,
		// before it is provisioned: a launch that did not wait answers
		// only then, and this command reports the same.
		case res.Run.Status == "queued" || res.Run.Status == "provisioning":
		case res.Run.Status == "failed":
			return protocol.RunLaunchResult{}, &errWaitEnded{code: 1, text: fmt.Sprintf("session %s started once the server had room and failed: %s", res.Run.ID, res.Run.Reason)}
		default:
			// The replay is this command's own launch, not an earlier one.
			res.Replayed = false
			return res, nil
		}
		select {
		case <-time.After(interval):
		case <-interrupt:
			if res.Waiting == nil {
				// Its session exists: an interrupt leaves it, as it leaves any launch.
				return protocol.RunLaunchResult{}, &errWaitEnded{code: 130, text: fmt.Sprintf("interrupted; session %s is starting: aether kill %s stops it", res.Run.ID, res.Run.ID)}
			}
			return protocol.RunLaunchResult{}, cancel()
		}
	}
}

func dropDigits(r rune) rune {
	if r >= '0' && r <= '9' {
		return -1
	}
	return r
}

var errInterrupted = errors.New("interrupted")

// The server finishes a call the user interrupts, except a launch it had
// not yet admitted, so callInterruptibly does not wait for it.
func callInterruptibly(c *protocol.Client, method string, params, result any) error {
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	done := make(chan error, 1)
	go func() { done <- c.Call(method, params, result) }()
	select {
	case err := <-done:
		return err
	case <-interrupt:
		return errInterrupted
	}
}

// Without the server's answer the launch may be going on, so only then
// does launchError name the retry.
func launchError(err error, params protocol.RunLaunchParams, fromStdin bool) error {
	var rpcErr *protocol.Error
	var ended *errWaitEnded
	switch {
	case errors.As(err, &ended):
		return &exitStatusError{code: ended.code, err: err}
	case errors.Is(err, errInterrupted):
		return &exitStatusError{code: 130, err: fmt.Errorf("%w; a launch the server had already accepted keeps starting: aether sessions lists it, and aether kill <session-id> stops it once it is listed.\nto get its session id, or to launch it once if the server had not accepted it, run:\n  %s", errInterrupted, launchCommand(params, fromStdin))}
	case errors.As(err, &rpcErr):
		return cachedBaseLaunchError(err, params, fromStdin)
	}
	if params.Wait {
		return fmt.Errorf("%w\nthe launch may still be starting, or waiting for room on the server; aether sessions lists it once it has started. To follow it without launching twice, run:\n  %s", err, launchCommand(params, fromStdin))
	}
	return fmt.Errorf("%w\nthe launch may still be starting; aether sessions lists it. To retry without launching twice, run:\n  %s", err, launchCommand(params, fromStdin))
}

// The server fingerprints every launch param but the key, so
// launchCommand must reproduce params exactly or a retry with the same key
// conflicts.
func launchCommand(params protocol.RunLaunchParams, fromStdin bool) string {
	args := []string{"aether", "session", "--agent", shellquote.Quote(params.Harness)}
	if params.Mode != "" {
		args = append(args, "--mode", modeName(params.Mode))
	}
	if params.Model != "" {
		args = append(args, "--model", shellquote.Quote(params.Model))
	}
	if params.Effort != "" {
		args = append(args, "--effort", shellquote.Quote(params.Effort))
	}
	if params.Permission != "" {
		args = append(args, "--permission", shellquote.Quote(params.Permission))
	}
	args = append(args, "--workspace", shellquote.Quote(params.WorkspaceID))
	if params.AccountMemberID != "" {
		args = append(args, "--account", shellquote.Quote(params.AccountMemberID))
	}
	if len(params.FallbackAccountMemberIDs) > 0 {
		args = append(args, "--fallback", shellquote.Quote(strings.Join(params.FallbackAccountMemberIDs, ",")))
	}
	if params.NoFallback {
		args = append(args, "--no-fallback")
	}
	for _, id := range params.ExtraWorkspaceIDs {
		args = append(args, "--repo", shellquote.Quote(id))
	}
	if params.CachedBase != "" {
		args = append(args, "--cached-base", shellquote.Quote(params.CachedBase))
	}
	if params.Children != "" {
		args = append(args, "--children", params.Children)
	}
	if params.LinearIssueURL != "" {
		args = append(args, "--linear-issue", shellquote.Quote(params.LinearIssueURL))
	}
	if !params.Wait {
		args = append(args, "--no-wait")
	}
	if params.IdempotencyKey != "" {
		args = append(args, "--idempotency-key", shellquote.Quote(params.IdempotencyKey))
	}
	if fromStdin {
		return strings.Join(append(args, "--", "-"), " ") + " < <the same task file>"
	}
	return strings.Join(append(args, "--", shellquote.Quote(params.Task)), " ")
}

func launchParams(workspace, task, agent, mode, model, effort, account, cachedBase string) protocol.RunLaunchParams {
	return protocol.RunLaunchParams{
		WorkspaceID:     workspace,
		Task:            task,
		Harness:         agent,
		Mode:            mode,
		Model:           model,
		Effort:          effort,
		AccountMemberID: account,
		CachedBase:      cachedBase,
	}
}

const (
	fallbackHelp   = "member IDs to launch on, in order, when the account's usage is exhausted, comma separated (default: every account you may use, most usage left first)"
	noFallbackHelp = "launch only on the requested account, even when its usage is exhausted"
)

func parseFallback(order string, none bool) ([]string, error) {
	if order == "" {
		return nil, nil
	}
	if none {
		return nil, errors.New("--fallback and --no-fallback cannot be used together")
	}
	ids := strings.Split(order, ",")
	for i, id := range ids {
		ids[i] = strings.TrimSpace(id)
		if ids[i] == "" {
			return nil, fmt.Errorf("--fallback %q has an empty member ID", order)
		}
	}
	return ids, nil
}

func setupOutput(err error) string {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || len(rpcErr.Data) == 0 {
		return ""
	}
	var failure protocol.SetupFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil {
		return ""
	}
	return failure.SetupOutput
}

// cachedBaseLaunchError turns a structured base-capture refusal into an
// explicit, copyable retry without changing ordinary RPC error handling.
// The retry differs from the refused launch, so it takes a new key.
func cachedBaseLaunchError(err error, params protocol.RunLaunchParams, fromStdin bool) error {
	var rpcErr *protocol.Error
	if !errors.As(err, &rpcErr) || rpcErr == nil || len(rpcErr.Data) == 0 {
		return err
	}
	var failure protocol.MirrorFailure
	if json.Unmarshal(rpcErr.Data, &failure) != nil || failure.AcceptedCommit == "" {
		return err
	}
	params.CachedBase, params.IdempotencyKey = failure.AcceptedCommit, ""
	return fmt.Errorf("%s\nretry from cached %s with:\n  %s",
		rpcErr.Message, failure.AcceptedCommit, launchCommand(params, fromStdin))
}
