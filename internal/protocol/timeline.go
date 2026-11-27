package protocol

// Wave 4 workspace timeline method.
const (
	// MethodWorkspaceTimeline pages one workspace's persisted history.
	MethodWorkspaceTimeline = "workspace.timeline"
)

// WorkspaceTimelineParams selects one page of a workspace's history. RunID,
// MemberID (the event's actor), and Types narrow it; AfterSeq is the
// cursor from the previous page's NextSeq, zero for the beginning. Newest
// pages backward instead: the newest matches before BeforeSeq (zero: the
// log head), where BeforeSeq is the previous page's OlderSeq. Limit zero
// means the server's default page size.
type WorkspaceTimelineParams struct {
	WorkspaceID string   `json:"workspace_id"`
	RunID       string   `json:"run_id,omitempty"`
	MemberID    string   `json:"member_id,omitempty"`
	Types       []string `json:"types,omitempty"`
	AfterSeq    uint64   `json:"after_seq,omitempty"`
	Newest      bool     `json:"newest,omitempty"`
	BeforeSeq   uint64   `json:"before_seq,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	// NoRereads leaves out run.status rereads even when Types names
	// run.status. A server that predates the field ignores it and returns them.
	NoRereads bool `json:"no_rereads,omitempty"`
}

// WorkspaceTimelineResult is one page of history, oldest first, in the same
// Event envelope the event feed streams. More reports that history
// remains after NextSeq - a page can be empty and still have more, since
// filtered-out events advance the cursor too. A Newest page sets NextSeq
// to the log head, and More reports older history before OlderSeq.
// OlderTime is the time of the event at OlderSeq, when the read stopped on
// one: the page covers everything after it, including what a filter left
// out, so a reader can tell a stretch with nothing to show from one not read.
type WorkspaceTimelineResult struct {
	Events    []Event `json:"events"`
	NextSeq   uint64  `json:"next_seq"`
	More      bool    `json:"more"`
	OlderSeq  uint64  `json:"older_seq,omitempty"`
	OlderTime string  `json:"older_time,omitempty"`
}
