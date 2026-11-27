package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/shellquote"
	"golang.org/x/term"
)

// addedLoginsAgent is the one agent whose logins the subcommands of
// cloudfield agent logins act on: only Claude Code has added logins.
const addedLoginsAgent = "claude"

// addLogin reserves the login, runs its sign-in in the terminal the server
// opens for it, and prints the login the server added or its refusal.
func addLogin(out io.Writer, agent protocol.AgentInfo, name string) error {
	return withSignIn(func(conn *cli.Conn, c *protocol.Client) error {
		var added protocol.AgentLoginAddResult
		if err := c.Call(protocol.MethodAgentLoginAdd, protocol.AgentLoginAddParams{Agent: agent.Name, Name: name}, &added); err != nil {
			return addRefused(olderServer(err), agent.Name)
		}
		if _, err := fmt.Fprintf(out, "adding login %s as %s: %s runs in a container of its own; follow its prompts\n"+
			"if this terminal goes before the sign-in ends, return to it with: %s\n",
			name, added.LoginID, agent.LoginCommand, attachCommand(agent.Name, added.LoginID)); err != nil {
			return err
		}
		return signIn(out, conn, c, agent.Name, added.LoginID)
	})
}

// attachLogin reopens the terminal of a sign-in addLogin started.
func attachLogin(out io.Writer, agent protocol.AgentInfo, id string) error {
	return withSignIn(func(conn *cli.Conn, c *protocol.Client) error {
		if _, err := fmt.Fprintf(out, "returning to the sign-in of login %s: %s runs in a container of its own; follow its prompts\n", id, agent.LoginCommand); err != nil {
			return err
		}
		return signIn(out, conn, c, agent.Name, id)
	})
}

// signInCommand is the cloudfield agent login line that passes value to
// flag, quoted for a shell.
func signInCommand(agent, flag, value string) string {
	return "cloudfield agent login " + shellquote.Quote(agent) + " " + flag + " " + shellquote.Quote(value)
}

func attachCommand(agent, id string) string {
	return signInCommand(agent, "--attach", id)
}

// withSignIn runs fn on a connection and its control channel. An
// interrupted call may be stuck writing to SSH, so the connection closes
// before the channel.
func withSignIn(fn func(conn *cli.Conn, c *protocol.Client) error) error {
	cfg, err := cli.Load()
	if err != nil {
		return err
	}
	conn, err := cli.Dial(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	c, err := conn.Control()
	if err != nil {
		return err
	}
	err = fn(conn, c)
	if errors.Is(err, errInterrupted) {
		_ = conn.Close()
	}
	_ = c.Close()
	return err
}

// signIn opens the sign-in terminal of login id, then prints the login
// the server added. Whenever the sign-in may still be running, the error
// names the command that returns to it.
func signIn(out io.Writer, conn *cli.Conn, c *protocol.Client, agent, id string) error {
	cols, rows := termSize()
	return followSignIn(out, os.Stderr, c, agent, id, func() (*cli.TerminalStream, protocol.TerminalResponse, error) {
		return conn.TerminalStream(protocol.TerminalRequest{AgentLogin: id, Cols: cols, Rows: rows})
	})
}

// signInRetryPause is how long a refused sign-in terminal waits for the
// sign-in's result before it is opened again; signInRetryLimit bounds the
// retries of a sign-in still starting, the opens included.
var (
	signInRetryPause = 5 * time.Second
	signInRetryLimit = time.Minute
)

var (
	errNotEnded      = errors.New("the sign-in has not ended")
	errStillStarting = errors.New("the sign-in is still starting")
)

// followSignIn waits for the sign-in's result while it opens the terminal:
// the server refuses a sign-in that ended and one still starting with the
// same code, and only one that ended has a result. A terminal refused as
// unavailable belongs to a sign-in that is ending, which has a result
// soon.
func followSignIn(out, errOut io.Writer, c *protocol.Client, agent, id string, open func() (*cli.TerminalStream, protocol.TerminalResponse, error)) error {
	var login protocol.AgentLogin
	result := make(chan error, 1)
	go func() {
		result <- c.Call(protocol.MethodAgentLoginResult, protocol.AgentLoginParams{Agent: agent, LoginID: id}, &login)
	}()
	ended := func(wait <-chan time.Time) error {
		return interruptibly(func() error {
			select {
			case err := <-result:
				return err
			case <-wait:
				return errNotEnded
			}
		})
	}
	var deadline time.Time
	for {
		var stream *cli.TerminalStream
		var ack protocol.TerminalResponse
		err := interruptibly(func() error {
			var expired <-chan time.Time
			if !deadline.IsZero() {
				expired = time.After(time.Until(deadline))
			}
			opened := make(chan error, 1)
			go func() {
				s, a, err := open()
				stream, ack = s, a
				opened <- err
			}()
			select {
			case err := <-opened:
				return err
			case <-expired:
				return errStillStarting
			}
		})
		switch {
		case errors.Is(err, errInterrupted):
			return mayStillRun(err, agent, id)
		case errors.Is(err, errStillStarting):
			return stillStarting(agent, id)
		case err == nil:
			err = describeTerminalEnd(copyRaw(stream, 0))
		case ack.Code == protocol.CodeNotFound:
			return err
		case ack.Code != 0 && ack.Code != protocol.CodeUnavailable:
			starting := ack.Code == protocol.CodeConflict
			first := starting && deadline.IsZero()
			if first {
				deadline = time.Now().Add(signInRetryLimit)
			}
			wait := signInRetryPause
			if starting {
				wait = min(wait, time.Until(deadline))
			}
			resultErr := ended(time.After(wait))
			switch {
			case !errors.Is(resultErr, errNotEnded):
				return signInEnded(out, agent, id, &login, resultErr)
			case !starting:
				return err
			case !time.Now().Before(deadline):
				return stillStarting(agent, id)
			case first:
				if _, werr := fmt.Fprintf(errOut, "%v\ntrying again every %v for up to %v; Ctrl-C stops trying and leaves the sign-in running\n", err, signInRetryPause, signInRetryLimit); werr != nil {
					return werr
				}
			}
			continue
		}
		if err != nil {
			if _, werr := fmt.Fprintf(errOut, "%v\nwaiting for the sign-in of login %s to end; Ctrl-C stops waiting and leaves it running\n", err, id); werr != nil {
				return werr
			}
		}
		return signInEnded(out, agent, id, &login, ended(nil))
	}
}

func stillStarting(agent, id string) error {
	return fmt.Errorf("the sign-in of login %s is still starting after %v; open its terminal again with:\n  %s", id, signInRetryLimit, attachCommand(agent, id))
}

// signInEnded prints the login the sign-in added, or returns why it added
// none.
func signInEnded(out io.Writer, agent, id string, login *protocol.AgentLogin, err error) error {
	if err != nil {
		var rpc *protocol.Error
		if errors.As(err, &rpc) {
			return err
		}
		return mayStillRun(err, agent, id)
	}
	lines := []string{"added login " + describeLogin(*login)}
	charged := ""
	if login.PerUse {
		charged = ", and those sessions are charged per use to it"
	}
	name := shellquote.Quote(login.Name)
	if login.Automatic {
		lines = append(lines, "Automatic is on: Cloudfield may start or move your sessions onto it"+charged+"; turn it off with: cloudfield agent logins auto "+name+" off")
	}
	if login.Shared {
		lines = append(lines, "Use for shares is on: teammates you share your account with can be served by it"+charged+"; turn it off with: cloudfield agent logins share "+name+" off")
	}
	_, err = fmt.Fprintln(out, strings.Join(lines, "\n"))
	return err
}

func mayStillRun(err error, agent, id string) error {
	err = fmt.Errorf("%w\nthe sign-in of login %s may still be running on the server; return to it with:\n  %s", err, id, attachCommand(agent, id))
	if errors.Is(err, errInterrupted) {
		return &exitStatusError{code: 130, err: err}
	}
	return err
}

// loginIDText is the form of an added login's id.
var loginIDText = regexp.MustCompile(`lgn-[a-z0-9]+`)

// addRefused adds to the refusal of a second sign-in how the first one
// ends. A login's name may look like an id, so the id is the last one the
// server names; a server that names none leaves the person the line the
// first sign-in printed.
func addRefused(err error, agent string) error {
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeConflict || !strings.Contains(rpc.Message, "is still running") {
		return err
	}
	const ends = "that sign-in ends by itself 15 minutes after its terminal opened, or 10 minutes after it started if none did"
	if ids := loginIDText.FindAllString(rpc.Message, -1); len(ids) > 0 {
		return fmt.Errorf("%w\n%s; to finish it sooner, return to it with:\n  %s", err, ends, attachCommand(agent, ids[len(ids)-1]))
	}
	return fmt.Errorf("%w\n%s; to finish it sooner, run the cloudfield agent login %s --attach <login-id> line it printed when it started", err, ends, shellquote.Quote(agent))
}

// describeLogin is a login's name and what Claude Code recorded at sign-in.
func describeLogin(l protocol.AgentLogin) string {
	parts := []string{cmp.Or(l.Kind, "kind unknown")}
	if l.Plan != "" {
		parts = append(parts, "plan "+l.Plan)
	}
	if l.Email != "" {
		parts = append(parts, l.Email)
	}
	if org := cmp.Or(l.OrganizationName, l.OrganizationID); org != "" {
		parts = append(parts, "organisation "+org)
	}
	if l.PerUse {
		parts = append(parts, "billed per use")
	}
	return l.Name + ": " + strings.Join(parts, ", ")
}

func agentLogins(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "rename":
			return renameLogin(args[1:])
		case "remove":
			return removeLogin(args[1:])
		case "order":
			return orderLogins(args[1:])
		case "auto":
			return automaticLogin(args[1:])
		case "share":
			return shareLogin(args[1:])
		}
	}
	const usage = "usage: cloudfield agent logins [<agent>] [--json]"
	fs := newFlagSet("agent logins")
	jsonOut := fs.Bool("json", false, "print the logins, their usage and the ids of your sessions on each as JSON")
	agent := addedLoginsAgent
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		agent, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New(usage)
	}
	return withControl(func(c *protocol.Client) error {
		logins, err := readLogins(c, agent, os.Stderr)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(logins)
		}
		return printLogins(os.Stdout, logins)
	})
}

// loginUsage is a login, its usage as account.usage read it or the error it
// returned, and the caller's sessions on it that have not ended, nil when
// they could not be read.
type loginUsage struct {
	protocol.AgentLogin
	Usage      *protocol.UsageProvider `json:"usage,omitempty"`
	UsageError string                  `json:"usage_error,omitempty"`
	Sessions   []string                `json:"sessions"`
}

// readLogins writes to errOut why the caller's sessions could not be read,
// and lists the logins without them.
func readLogins(c *protocol.Client, agent string, errOut io.Writer) ([]loginUsage, error) {
	logins, err := listLogins(c, agent)
	if err != nil {
		return nil, err
	}
	var info protocol.ServerInfoResult
	var runs protocol.RunListResult
	sessionsErr := c.Call(protocol.MethodServerInfo, struct{}{}, &info)
	if sessionsErr == nil {
		sessionsErr = c.Call(protocol.MethodRunList, protocol.RunListParams{MemberID: info.Member.ID, ActiveOnly: true}, &runs)
	}
	if sessionsErr != nil {
		_, _ = fmt.Fprintf(errOut, "your sessions on each login could not be read: %v\n", sessionsErr)
	}
	usage := make([]loginUsage, len(logins))
	for i, l := range logins {
		usage[i].AgentLogin = l
		if sessionsErr == nil {
			usage[i].Sessions = []string{}
		}
		for _, r := range runs.Runs {
			// A run on the default login records no login.
			if r.AccountMemberID == info.Member.ID && r.Harness == agent && cmp.Or(r.AccountLogin, string(domain.DefaultLogin)) == l.ID {
				usage[i].Sessions = append(usage[i].Sessions, r.ID)
			}
		}
		var read protocol.AccountUsageResult
		if err := c.Call(protocol.MethodAccountUsage, protocol.AccountUsageParams{Login: l.ID}, &read); err != nil {
			usage[i].UsageError = err.Error()
			continue
		}
		for _, p := range read.Providers {
			if p.Provider == agent {
				usage[i].Usage = &p
			}
		}
	}
	return usage, nil
}

func printLogins(w io.Writer, logins []loginUsage) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tLOGIN\tKIND\tPLAN\tEMAIL\tORGANISATION\tUSAGE\tAUTOMATIC\tSHARES\tBILLED\tYOUR SESSIONS")
	var problems []string
	for i, l := range logins {
		usage := "-"
		switch {
		case l.UsageError != "":
			usage = "not read"
			problems = append(problems, fmt.Sprintf("usage of login %s: %s", l.Name, l.UsageError))
		case l.Usage != nil && len(l.Usage.Windows) > 0:
			used := make([]string, 0, len(l.Usage.Windows))
			for _, win := range l.Usage.Windows {
				used = append(used, fmt.Sprintf("%s %.0f%%", win.Label, win.UsedPercent))
			}
			usage = strings.Join(used, ", ")
		case l.Usage != nil && l.Usage.Status != "ok":
			usage = l.Usage.Status
		}
		if l.Usage != nil && l.Usage.Error != "" {
			problems = append(problems, fmt.Sprintf("usage of login %s: %s", l.Name, l.Usage.Error))
		}
		billed := "-"
		if l.PerUse {
			billed = "per use"
		}
		sessions := "-"
		if l.Sessions != nil {
			sessions = strconv.Itoa(len(l.Sessions))
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, l.Name, cmp.Or(l.Kind, "-"), cmp.Or(l.Plan, "-"), cmp.Or(l.Email, "-"),
			cmp.Or(l.OrganizationName, l.OrganizationID, "-"), usage, onOff(l.Automatic), onOff(l.Shared), billed, sessions)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, p := range problems {
		if _, err := fmt.Fprintln(w, p); err != nil {
			return err
		}
	}
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// requireLogins refuses flag on a server that keeps no added logins.
func requireLogins(info protocol.ServerInfoResult, flag string) error {
	if slices.Contains(info.Features, protocol.FeatureLogins) {
		return nil
	}
	return fmt.Errorf("%s: server %s lacks the %q feature, so it keeps only the login in your home; update the server with cloudfield server update", flag, info.ServerVersion, protocol.FeatureLogins)
}

func listLogins(c *protocol.Client, agent string) ([]protocol.AgentLogin, error) {
	var list protocol.AgentLoginListResult
	if err := c.Call(protocol.MethodAgentLoginList, protocol.AgentLoginParams{Agent: agent}, &list); err != nil {
		return nil, err
	}
	return list.Logins, nil
}

// loginID resolves one of the caller's logins, named by its id or its name,
// to its id. An id wins: a name taken before ids were reserved may look
// like another login's id, and the id is how either is still reached.
func loginID(logins []protocol.AgentLogin, name string) (string, error) {
	if i := slices.IndexFunc(logins, func(l protocol.AgentLogin) bool { return l.ID == name }); i >= 0 {
		return logins[i].ID, nil
	}
	if i := slices.IndexFunc(logins, func(l protocol.AgentLogin) bool { return l.Name == name }); i >= 0 {
		return logins[i].ID, nil
	}
	return "", fmt.Errorf("you have no login named %q; cloudfield agent logins lists yours", name)
}

var loginIDForm = regexp.MustCompile(`^` + loginIDText.String() + `$`)

// launchLogin is the login run.launch names for the --login value. An id
// goes as given, so the retry a launch printed still names its login after
// a rename or a removal; so does a value on a teammate's account, which
// only its owner names and the server refuses. A name of one of the
// caller's own logins resolves here.
func launchLogin(c *protocol.Client, me, agent, account, value string) (string, error) {
	if value == string(domain.DefaultLogin) || loginIDForm.MatchString(value) || (account != "" && account != me) {
		return value, nil
	}
	logins, err := listLogins(c, agent)
	if err != nil {
		return "", err
	}
	id, err := loginID(logins, value)
	if err != nil {
		return "", fmt.Errorf("--login: %w", err)
	}
	return id, nil
}

// landedLines say where a launch that named a login started when that is
// not the login named, which fallback accounts it left out, and for any
// launch that its login is billed per use.
func landedLines(res protocol.RunLaunchResult, named string, nameOf func(memberID string) string) []string {
	run := res.Run
	landed := cmp.Or(run.AccountLogin, string(domain.DefaultLogin))
	moved := named != "" && (run.RequestedAccountMemberID != "" || (named != landed && named != run.AccountLoginName))
	var lines []string
	if moved || run.AccountLoginPerUse {
		where := "your login "
		if run.AccountMemberID != run.MemberID {
			where = nameOf(run.AccountMemberID) + "'s account, login "
		}
		line := "started on " + where + cmp.Or(run.AccountLoginName, landed)
		if moved {
			line += ", not " + named
		}
		if run.AccountLoginPerUse {
			line += "; billed per use"
		}
		lines = append(lines, line)
	}
	if named == "" {
		return lines
	}
	for _, s := range res.SkippedFallbackAccounts {
		if s.AccountMemberID == "" {
			lines = append(lines, s.Error)
			continue
		}
		lines = append(lines, "left out "+nameOf(s.AccountMemberID)+"'s account: "+s.Error)
	}
	return lines
}

// printLanded prints landedLines, reading member names only if a line
// needs one; a failed read leaves the member's id and its error on stderr.
func printLanded(c *protocol.Client, res protocol.RunLaunchResult, named string) {
	var names map[string]string
	nameOf := func(id string) string {
		if names == nil {
			var err error
			if names, err = memberNames(c); err != nil {
				_, _ = fmt.Fprintln(os.Stderr, err)
				names = map[string]string{}
			}
		}
		return nameOrID(names, id)
	}
	for _, line := range landedLines(res, named, nameOf) {
		fmt.Println(line)
	}
}

// withLogins runs fn with the caller's logins of Claude Code.
func withLogins(fn func(c *protocol.Client, logins []protocol.AgentLogin) error) error {
	return withControl(func(c *protocol.Client) error {
		logins, err := listLogins(c, addedLoginsAgent)
		if err != nil {
			return err
		}
		return fn(c, logins)
	})
}

func renameLogin(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: cloudfield agent logins rename <login> <new-name>")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		id, err := loginID(logins, args[0])
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodAgentLoginRename, protocol.AgentLoginRenameParams{Agent: addedLoginsAgent, LoginID: id, Name: args[1]}, nil); err != nil {
			return err
		}
		fmt.Printf("renamed login %s to %s\n", args[0], args[1])
		return nil
	})
}

func removeLogin(args []string) error {
	yes := len(args) == 2 && args[1] == "--yes"
	if len(args) != 1 && !yes {
		return errors.New("usage: cloudfield agent logins remove <login> [--yes]")
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd()))
	if !yes && !interactive {
		return errors.New("--yes is required because cloudfield agent logins remove deletes the login's files from Cloudfield: cloudfield agent logins remove " + shellquote.Quote(args[0]) + " --yes")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		return removeLoginOf(c, logins, args[0], yes, os.Stdin, os.Stdout)
	})
}

func removeLoginOf(c *protocol.Client, logins []protocol.AgentLogin, name string, yes bool, in io.Reader, out io.Writer) error {
	id, err := loginID(logins, name)
	if err != nil {
		return err
	}
	if !yes {
		login := logins[slices.IndexFunc(logins, func(l protocol.AgentLogin) bool { return l.ID == id })]
		_, _ = fmt.Fprintf(out, "login %s\nRemoving it deletes its files from Cloudfield and does not sign it out at Anthropic; "+
			"Cloudfield refuses while a session is on it. Remove? [y/N]: ", describeLogin(login))
		if !yesAnswer(bufio.NewReader(in)) {
			return errors.New("cloudfield agent logins remove: nothing was removed")
		}
	}
	if err = c.Call(protocol.MethodAgentLoginRemove, protocol.AgentLoginParams{Agent: addedLoginsAgent, LoginID: id}, nil); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "removed login %s and its files from Cloudfield; this does not sign it out at Anthropic\n", name)
	return err
}

func orderLogins(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cloudfield agent logins order <login> [<login> ...]")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		ids := make([]string, len(args))
		for i, name := range args {
			id, err := loginID(logins, name)
			if err != nil {
				return err
			}
			ids[i] = id
		}
		if err := c.Call(protocol.MethodAgentLoginOrder, protocol.AgentLoginOrderParams{Agent: addedLoginsAgent, LoginIDs: ids}, nil); err != nil {
			return err
		}
		order := make([]string, len(args))
		for i, name := range args {
			order[i] = strconv.Itoa(i+1) + " " + name
		}
		fmt.Printf("login order: %s\n", strings.Join(order, ", "))
		return nil
	})
}

func automaticLogin(args []string) error {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return errors.New("usage: cloudfield agent logins auto <login> on|off")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		id, err := loginID(logins, args[0])
		if err != nil {
			return err
		}
		on := args[1] == "on"
		if err := c.Call(protocol.MethodAgentLoginAutomatic, protocol.AgentLoginAutomaticParams{Agent: addedLoginsAgent, LoginID: id, Automatic: &on}, nil); err != nil {
			return err
		}
		if on {
			fmt.Printf("login %s: Cloudfield may choose it and move sessions onto it\n", args[0])
		} else {
			fmt.Printf("login %s: used only when you name it with cloudfield session --agent %s --login %s\n", args[0], addedLoginsAgent, shellquote.Quote(args[0]))
		}
		return nil
	})
}

func shareLogin(args []string) error {
	if len(args) != 2 || (args[1] != "on" && args[1] != "off") {
		return errors.New("usage: cloudfield agent logins share <login> on|off")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		id, err := loginID(logins, args[0])
		if err != nil {
			return err
		}
		on := args[1] == "on"
		if err := c.Call(protocol.MethodAgentLoginShare, protocol.AgentLoginShareParams{Agent: addedLoginsAgent, LoginID: id, Shared: &on}, nil); err != nil {
			return err
		}
		if on {
			fmt.Printf("login %s: members you share your account with may run on it (cloudfield account list names them)\n", args[0])
		} else {
			fmt.Printf("login %s: only your own sessions run on it; Cloudfield moves teammates' sessions off it, and stops those no other login can run\n", args[0])
		}
		return nil
	})
}
