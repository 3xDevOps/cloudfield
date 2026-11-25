package protocol

// The Linear methods let a member save a personal Linear API key on the
// server and read the Linear issues assigned to them (docs/linear.md).
// Each acts on the caller's own key. Only a member's own credentials reach
// them: a session's coordination socket and connectors do not.
const (
	// MethodLinearStatus reports whether the caller saved a key and whose it is.
	MethodLinearStatus = "linear.status"
	// MethodLinearConnect validates a key with Linear and saves it.
	MethodLinearConnect = "linear.connect"
	// MethodLinearDisconnect deletes the caller's key.
	MethodLinearDisconnect = "linear.disconnect"
	// MethodLinearTeams lists the teams the caller's key can see.
	MethodLinearTeams = "linear.teams"
	// MethodLinearIssues lists the open issues assigned to the caller.
	MethodLinearIssues = "linear.issues"
	// MethodLinearIssue reads one issue by identifier, such as CLO-6.
	MethodLinearIssue = "linear.issue"
)

// LinearStatusResult is the caller's Linear connection. The other fields
// are empty unless Connected.
type LinearStatusResult struct {
	Connected    bool   `json:"connected"`
	Name         string `json:"name,omitempty"`
	Organization string `json:"organization,omitempty"`
	URLKey       string `json:"url_key,omitempty"`
}

type LinearConnectParams struct {
	APIKey string `json:"api_key"`
}

// LinearIssuesParams narrows the list to one team key, such as CLO, and to
// issues whose title contains Query, ignoring case.
type LinearIssuesParams struct {
	Team  string `json:"team,omitempty"`
	Query string `json:"query,omitempty"`
}

type LinearIssueParams struct {
	Identifier string `json:"identifier"`
}

type LinearTeam struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type LinearTeamsResult struct {
	Teams []LinearTeam `json:"teams"`
}

type LinearState struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Color string `json:"color,omitempty"`
}

type LinearAssignee struct {
	Name string `json:"name"`
}

// LinearIssue is one issue. Team, Assignee and UpdatedAt are empty in the
// result of linear.issue, which reads only the identifier, title, URL and
// state.
type LinearIssue struct {
	Identifier string          `json:"identifier"`
	Title      string          `json:"title"`
	URL        string          `json:"url"`
	State      LinearState     `json:"state"`
	Team       *LinearTeam     `json:"team,omitempty"`
	Assignee   *LinearAssignee `json:"assignee,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

type LinearIssuesResult struct {
	Issues []LinearIssue `json:"issues"`
}
