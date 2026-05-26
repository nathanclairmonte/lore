// Package errors provides typed errors carrying a semantic exit code.
// Every command handler returns plain error; main.go uses CodeOf to translate
// to the global exit code table when calling os.Exit.
package errors

import (
	stderrors "errors"
	"fmt"
)

// Error carries a semantic exit code, a user-facing message, an optional
// next-step hint, and an optional wrapped cause.
type Error struct {
	Code    ExitCode
	Message string
	Hint    string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// New constructs a lore error with no underlying cause.
func New(code ExitCode, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// Newf is New with fmt.Sprintf formatting.
func Newf(code ExitCode, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a cause to a new lore error.
func Wrap(code ExitCode, cause error, msg string) *Error {
	return &Error{Code: code, Message: msg, Cause: cause}
}

// WithHint returns err with its next-step hint set. Chains naturally with
// constructors: errors.New(...).WithHint("...").
func (e *Error) WithHint(hint string) *Error {
	e.Hint = hint
	return e
}

// CodeOf walks the error chain and returns the first ExitCode found, or
// CodeGenericError if no *Error appears in the chain. Returns CodeSuccess
// only when err is nil.
func CodeOf(err error) ExitCode {
	if err == nil {
		return CodeSuccess
	}
	var le *Error
	if stderrors.As(err, &le) {
		return le.Code
	}
	return CodeGenericError
}

// HintOf returns the hint of the first *Error in the chain, or "" if none.
func HintOf(err error) string {
	var le *Error
	if stderrors.As(err, &le) {
		return le.Hint
	}
	return ""
}
