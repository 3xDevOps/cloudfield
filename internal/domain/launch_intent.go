package domain

import (
	"encoding/json"
	"time"
)

// LaunchIntentState is where a launch that waits for room stands. Waiting
// and starting are open; the rest are final.
type LaunchIntentState string

const (
	// IntentWaiting waits for its next attempt.
	IntentWaiting LaunchIntentState = "waiting"
	// IntentStarting is claimed by one attempt. It is not an outcome: a
	// server restart returns it to waiting unless its session exists.
	IntentStarting LaunchIntentState = "starting"
	// IntentStarted has its session, under the intent's ID.
	IntentStarted   LaunchIntentState = "started"
	IntentCancelled LaunchIntentState = "cancelled"
	// IntentExpired waited LaunchIntentLifetime without room.
	IntentExpired LaunchIntentState = "expired"
	// IntentRefused failed a check a fresh launch makes, at an attempt.
	IntentRefused LaunchIntentState = "refused"
)

// Open reports whether the intent may still become a session.
func (s LaunchIntentState) Open() bool { return s == IntentWaiting || s == IntentStarting }

// LaunchIntent is a launch the server had no room for, kept until there is.
// It is not a session: its ID becomes its session's only once it is started.
type LaunchIntent struct {
	// ID is the session ID reserved for it.
	ID          RunID
	MemberID    MemberID
	WorkspaceID WorkspaceID
	// IdempotencyKey is the request's, or the ID when it had none.
	IdempotencyKey string
	// Method and Params are the launch request as its member sent it,
	// without its key, replayed as that member at every attempt.
	Method string
	Params json.RawMessage
	// Base is the workspace base captured when the launch began to wait;
	// the session starts from it.
	Base LaunchBase
	// Via is the client the request came through.
	Via   string
	State LaunchIntentState
	// Refusal is the last refusal, as the caller got it; for a final state
	// that is not started, why it ended.
	Refusal string
	// Attempts counts the tries after the first refusal, and Passed the
	// launches admitted while it waited.
	Attempts    int
	Passed      int
	CreatedAt   time.Time
	AttemptedAt time.Time
	EndedAt     *time.Time
}

// LaunchBase is a workspace base as a session records it.
type LaunchBase struct {
	Commit    string
	Branch    string
	Source    string
	CheckedAt time.Time
}
