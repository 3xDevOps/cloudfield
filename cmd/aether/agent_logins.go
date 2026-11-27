package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/protocol"
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

func attachCommand(agent, id string) string {
	return "cloudfield agent login " + agent + " --attach " + id
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
	var stream *cli.TerminalStream
	err := interruptibly(func() (err error) {
		stream, _, err = conn.TerminalStream(protocol.TerminalRequest{AgentLogin: id, Cols: cols, Rows: rows})
		return err
	})
	if err == nil {
		err = describeTerminalEnd(copyRaw(stream, 0))
	}
	if err != nil {
		return mayStillRun(err, agent, id)
	}
	var login protocol.AgentLogin
	if err := callInterruptibly(c, protocol.MethodAgentLoginResult, protocol.AgentLoginParams{Agent: agent, LoginID: id}, &login); err != nil {
		var rpc *protocol.Error
		if errors.As(err, &rpc) {
			return err
		}
		return mayStillRun(err, agent, id)
	}
	_, err = fmt.Fprintf(out, "added login %s\n", describeLogin(login))
	return err
}

func mayStillRun(err error, agent, id string) error {
	err = fmt.Errorf("%w\nthe sign-in of login %s may still be running on the server; return to it with:\n  %s", err, id, attachCommand(agent, id))
	if errors.Is(err, errInterrupted) {
		return &exitStatusError{code: 130, err: err}
	}
	return err
}

// addRefused adds to the refusal of a second sign-in how the first one
// ends: the server names the running sign-in, not its id.
func addRefused(err error, agent string) error {
	var rpc *protocol.Error
	if !errors.As(err, &rpc) || rpc.Code != protocol.CodeConflict || !strings.Contains(rpc.Message, "is still running") {
		return err
	}
	return fmt.Errorf("%w\nthat sign-in ends by itself 15 minutes after its terminal opened, or 10 minutes after it started if none did; "+
		"to finish it sooner, run the %s line it printed when it started", err, attachCommand(agent, "<login-id>"))
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
	jsonOut := fs.Bool("json", false, "print the logins and their usage as JSON")
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
		logins, err := listLogins(c, agent)
		if err != nil {
			return err
		}
		usage := make([]loginUsage, len(logins))
		for i, l := range logins {
			usage[i].AgentLogin = l
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
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(usage)
		}
		return printLogins(os.Stdout, usage)
	})
}

// loginUsage is a login and its usage as account.usage read it, or the
// error it returned.
type loginUsage struct {
	protocol.AgentLogin
	Usage      *protocol.UsageProvider `json:"usage,omitempty"`
	UsageError string                  `json:"usage_error,omitempty"`
}

func printLogins(w io.Writer, logins []loginUsage) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "#\tLOGIN\tKIND\tPLAN\tEMAIL\tUSAGE\tAUTOMATIC\tSHARES\tBILLED")
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
			if l.Usage.Error != "" {
				problems = append(problems, fmt.Sprintf("usage of login %s: %s", l.Name, l.Usage.Error))
			}
		}
		billed := "-"
		if l.PerUse {
			billed = "per use"
		}
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", i+1, l.Name, cmp.Or(l.Kind, "-"), cmp.Or(l.Plan, "-"), cmp.Or(l.Email, "-"),
			usage, onOff(l.Automatic), onOff(l.Shared), billed)
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

func listLogins(c *protocol.Client, agent string) ([]protocol.AgentLogin, error) {
	var list protocol.AgentLoginListResult
	if err := c.Call(protocol.MethodAgentLoginList, protocol.AgentLoginParams{Agent: agent}, &list); err != nil {
		return nil, err
	}
	return list.Logins, nil
}

// loginID resolves the name of one of the caller's logins to its id; an
// id resolves to itself, so a retry a launch printed names the same login.
func loginID(logins []protocol.AgentLogin, name string) (string, error) {
	for _, l := range logins {
		if l.Name == name || l.ID == name {
			return l.ID, nil
		}
	}
	return "", fmt.Errorf("you have no login named %q; cloudfield agent logins lists yours", name)
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
	if len(args) != 1 {
		return errors.New("usage: cloudfield agent logins remove <login>")
	}
	return withLogins(func(c *protocol.Client, logins []protocol.AgentLogin) error {
		id, err := loginID(logins, args[0])
		if err != nil {
			return err
		}
		if err := c.Call(protocol.MethodAgentLoginRemove, protocol.AgentLoginParams{Agent: addedLoginsAgent, LoginID: id}, nil); err != nil {
			return err
		}
		fmt.Printf("removed login %s and its files from Cloudfield; this does not sign it out at Anthropic\n", args[0])
		return nil
	})
}

func orderLogins(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cloudfield agent logins order <login>...")
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
		if err := c.Call(protocol.MethodAgentLoginAutomatic, protocol.AgentLoginAutomaticParams{Agent: addedLoginsAgent, LoginID: id, Automatic: on}, nil); err != nil {
			return err
		}
		if on {
			fmt.Printf("login %s: Cloudfield may choose it and move sessions onto it\n", args[0])
		} else {
			fmt.Printf("login %s: used only when you name it with cloudfield session --login %s\n", args[0], args[0])
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
		if err := c.Call(protocol.MethodAgentLoginShare, protocol.AgentLoginShareParams{Agent: addedLoginsAgent, LoginID: id, Shared: on}, nil); err != nil {
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
