package events

import "github.com/3xDevOps/cloudfield/internal/domain"

// TypeRunUsageLimit reports that a run's agent stopped because its account
// hit a subscription usage limit.
const TypeRunUsageLimit Type = "run.usage_limit"

// RunUsageLimitPayload carries the agent's own usage-limit message and the
// session it stopped, which a move to another account resumes.
type RunUsageLimitPayload struct {
	Message   string `json:"message"`
	SessionID string `json:"session_id,omitempty"`
}

func (RunUsageLimitPayload) EventType() Type { return TypeRunUsageLimit }

func init() { registerPayload[RunUsageLimitPayload](TypeRunUsageLimit) }

// TypeRunAccount reports that a live run moved to another agent account.
const TypeRunAccount Type = "run.account"

// RunAccountPayload is a run's move from Previous to Account. Requested is
// the account the run first asked for, empty when it is Account; the run
// snapshot carries the same fields.
type RunAccountPayload struct {
	Account   domain.MemberID `json:"account_member_id"`
	Previous  domain.MemberID `json:"previous_account_member_id"`
	Requested domain.MemberID `json:"requested_account_member_id,omitempty"`
	// Login and PreviousLogin are the added logins of those accounts, empty
	// for a default login, with their names; PerUse is a login that now
	// pays per use.
	Login             domain.LoginID `json:"login,omitempty"`
	PreviousLogin     domain.LoginID `json:"previous_login,omitempty"`
	LoginName         string         `json:"login_name,omitempty"`
	PreviousLoginName string         `json:"previous_login_name,omitempty"`
	PerUse            bool           `json:"per_use,omitempty"`
}

func (RunAccountPayload) EventType() Type { return TypeRunAccount }

func init() { registerPayload[RunAccountPayload](TypeRunAccount) }
