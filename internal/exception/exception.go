// This package provides helper methods for producing errors which will mark
// tasks as exception-ing rather than failing. This is used in Taskcluster to
// indicate that it's the worker's fault, not the task's.
package exception

import "fmt"

// Error adds a way of indicating an internal worker exception.
type Error interface {
	error
	ExceptionKind() string
}

type exception struct {
	kind  string
	inner error
}

func (e *exception) Error() string {
	return fmt.Sprintf("Exception (%s) %v", e.kind, e.inner)
}

func (e *exception) Unwrap() error {
	return e.inner
}

func (e *exception) ExceptionKind() string {
	return e.kind
}

// WithKind creates a new exception object, wrapping an existing error.
func WithKind(kind string, inner error) Error {
	return &exception{
		kind:  kind,
		inner: inner,
	}
}

// InternalError creates an exception error with the 'internal-error' reason.
func InternalError(inner error) Error {
	return WithKind("internal-error", inner)
}

// MalformedPayload creates an exception error with the 'malformed-payload' reason.
func MalformedPayload(inner error) Error {
	return WithKind("malformed-payload", inner)
}
