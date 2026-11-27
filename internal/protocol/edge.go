package protocol

// MethodServerEdge reports the server's edge to any member.
const MethodServerEdge = "server.edge"

// ServerEdgeResult describes the server's edge. EdgeURL is empty when the
// server is not enrolled with an edge; ServerID is set either way. Owner
// is nil while the server has no owner at its edge. Claimed is whether
// the edge last reported the server as claimed. Notice, absent when there
// is none, is what the edge said about EdgeURL in its latest verified
// notices.
type ServerEdgeResult struct {
	EdgeURL      string       `json:"edge_url"`
	ServerID     string       `json:"server_id"`
	AccessPolicy string       `json:"access_policy"`
	Owner        *EdgeAccount `json:"owner"`
	Connected    bool         `json:"connected"`
	Claimed      bool         `json:"claimed"`
	Notice       *EdgeNotice  `json:"notice,omitempty"`
}

// EdgeNotice says that the server's edge address is retiring or retired
// and names the address of the same edge to move to. Date, YYYY-MM-DD or
// empty, is text for people. FirstUse is set when the notice came with
// the connection that pinned the edge key, so it is trusted only as that
// pin is, and for display.
type EdgeNotice struct {
	State       string `json:"state"`
	Replacement string `json:"replacement"`
	Date        string `json:"date,omitempty"`
	FirstUse    bool   `json:"first_use,omitempty"`
	// RestartPending is set when the server's options file names another
	// edge-url than the running one, as after edge move and before the
	// restart. MovedTo, for super admins only, is that edge-url,
	// canonical.
	RestartPending bool   `json:"restart_pending,omitempty"`
	MovedTo        string `json:"moved_to,omitempty"`
}

// EdgeAccount is an account signed in at an edge.
type EdgeAccount struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
}

// MethodServerEdgeClaimCode issues the calling admin a claim code that
// binds the account claiming with it to that admin's member.
const MethodServerEdgeClaimCode = "server.edge.claimcode"

// ServerEdgeClaimCodeResult is a claim code and when it expires, in
// RFC 3339.
type ServerEdgeClaimCodeResult struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expires_at"`
}
