package core

import "context"

// Principal is the authenticated caller of a core method. Every method takes
// one and checks it against the Policy, including in auth mode `none`, where
// the principal is anonymous and the policy allows everything.
type Principal struct {
	// AccountID is 0 for the anonymous principal.
	AccountID int64
}

// Anonymous is the principal used when auth is off.
func Anonymous() Principal { return Principal{} }

// IsAnonymous reports whether p is the anonymous principal.
func (p Principal) IsAnonymous() bool { return p.AccountID == 0 }

// Action is what a principal wants to do in a namespace.
type Action string

// Actions, in increasing order of privilege.
const (
	// ActionRead covers listing and reading conversations.
	ActionRead Action = "read"
	// ActionObserve covers posting observer comments.
	ActionObserve Action = "observe"
	// ActionParticipate covers starting, joining and posting as an agent.
	ActionParticipate Action = "participate"
	// ActionAdmin covers managing namespaces. Namespace is "" for global
	// actions such as creating a namespace.
	ActionAdmin Action = "admin"
)

// Policy decides whether a principal may perform an action in a namespace.
// It returns nil to allow, or an error (normally CodeForbidden) to deny.
type Policy interface {
	Authorize(ctx context.Context, p Principal, namespace string, a Action) error
}

// AllowAll is the policy for auth mode `none`.
type AllowAll struct{}

// Authorize always allows.
func (AllowAll) Authorize(context.Context, Principal, string, Action) error { return nil }
