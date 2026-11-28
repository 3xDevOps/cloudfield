package protocol

// A member's logins of an agent: the default login in their home and the
// logins they added. Only Claude Code has added logins. Every method acts on
// the caller's own logins; a login id of another member reads as not found.
const (
	// MethodAgentLoginList lists the caller's logins of an agent, the
	// default login first when they added none.
	MethodAgentLoginList = "agent.login.list"
	// MethodAgentLoginAdd starts adding a login: it reserves a login id,
	// whose sign-in then runs in a terminal opened with
	// TerminalRequest.AgentLogin.
	MethodAgentLoginAdd = "agent.login.add"
	// MethodAgentLoginResult waits for a sign-in to end and returns the new
	// login, or the reason it was not added.
	MethodAgentLoginResult    = "agent.login.result"
	MethodAgentLoginRename    = "agent.login.rename"
	MethodAgentLoginRemove    = "agent.login.remove"
	MethodAgentLoginOrder     = "agent.login.order"
	MethodAgentLoginAutomatic = "agent.login.automatic"
	// MethodAgentLoginShare sets whether the caller's account shares reach
	// a login; turning it off moves teammates' running sessions off it.
	MethodAgentLoginShare = "agent.login.share"
)

// AgentLoginShareParams sets a login's use-for-shares switch; Shared is
// required.
type AgentLoginShareParams struct {
	Agent   string `json:"agent"`
	LoginID string `json:"login_id"`
	Shared  *bool  `json:"shared"`
}

// AgentLoginParams names an agent, and for most methods one of the caller's
// logins of it: "default" or an added login's "lgn-" id.
type AgentLoginParams struct {
	Agent   string `json:"agent"`
	LoginID string `json:"login_id,omitempty"`
}

// AgentLoginAddParams names the agent and the new login.
type AgentLoginAddParams struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
}

// AgentLoginAddResult is the id the sign-in terminal opens with.
type AgentLoginAddResult struct {
	LoginID string `json:"login_id"`
}

type AgentLoginRenameParams struct {
	Agent   string `json:"agent"`
	LoginID string `json:"login_id"`
	Name    string `json:"name"`
}

// AgentLoginOrderParams is every one of the caller's logins of the agent,
// "default" included, first to last.
type AgentLoginOrderParams struct {
	Agent    string   `json:"agent"`
	LoginIDs []string `json:"login_ids"`
}

// AgentLoginAutomaticParams sets a login's automatic switch; Automatic is
// required.
type AgentLoginAutomaticParams struct {
	Agent     string `json:"agent"`
	LoginID   string `json:"login_id"`
	Automatic *bool  `json:"automatic"`
}

// AgentLoginListResult lists the logins in the order Cloudfield tries them.
type AgentLoginListResult struct {
	Agent  string       `json:"agent"`
	Logins []AgentLogin `json:"logins"`
}

// AgentLogin is one login. Kind is "subscription", "console" (a keyless
// Anthropic Console sign-in) or, for the default login only, "api_key";
// empty when Cloudfield finds none. Plan, Email and the organisation are
// what Claude Code recorded at sign-in, empty when it recorded none.
// PerUse is a login billed per use rather than by a subscription's windows.
// Shared is whether the caller's account shares reach the login.
type AgentLogin struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             string `json:"kind,omitempty"`
	Plan             string `json:"plan,omitempty"`
	Email            string `json:"email,omitempty"`
	OrganizationID   string `json:"organization_id,omitempty"`
	OrganizationName string `json:"organization_name,omitempty"`
	PerUse           bool   `json:"per_use,omitempty"`
	Automatic        bool   `json:"automatic"`
	Shared           bool   `json:"shared"`
	Position         int    `json:"position"`
}
