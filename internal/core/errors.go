package core

import (
	"errors"
	"fmt"
)

// Code identifies a class of failure. Codes are part of the public API: the
// HTTP and MCP layers report them verbatim.
type Code string

// Error codes.
const (
	CodeInvalid               Code = "invalid"
	CodeForbidden             Code = "forbidden"
	CodeNamespaceNotFound     Code = "namespace_not_found"
	CodeNamespaceExists       Code = "namespace_exists"
	CodeConversationNotFound  Code = "conversation_not_found"
	CodeConversationExists    Code = "conversation_exists"
	CodeConversationConcluded Code = "conversation_concluded"
	CodeNotParticipant        Code = "not_participant"
	CodeNameActive            Code = "name_active"
	CodeStaleContext          Code = "stale_context"
	CodeMessageTooLarge       Code = "message_too_large"
	CodeNotCoordinator        Code = "not_coordinator"
	CodeTooManyWaiters        Code = "too_many_waiters"
	CodeTooManyWatchers       Code = "too_many_watchers"
)

// Error is a failure the caller can act on. Message is short, ASCII, and
// says what to do next where there is something to do.
type Error struct {
	Code    Code
	Message string
	// LastSeq is set for CodeStaleContext: the conversation's current last seq.
	LastSeq int64
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

func errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the Code of err, or "" if err is not a core error.
func CodeOf(err error) Code {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code
	}
	return ""
}
