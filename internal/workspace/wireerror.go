package workspace

import "errors"

// wireError preserves a remote error's full message while unwrapping to a
// workspace sentinel, so both the human message and errors.Is classification
// survive a request/reply (vmm NATS) or command-socket round-trip.
type wireError struct {
	msg      string
	sentinel error
}

func (e *wireError) Error() string { return e.msg }
func (e *wireError) Unwrap() error { return e.sentinel }

// ErrorForCode reconstructs a sentinel-wrapped error from a vmm req.Error code
// (docs/reference/events.md), so a remote client returns errors that classify
// with errors.Is against the same sentinels a local Backend returns — Code(err)
// then re-derives the identical wire code. An unrecognized code yields a plain
// error carrying msg.
func ErrorForCode(code, msg string) error {
	var sentinel error
	switch code {
	case CodeUnsupported:
		sentinel = ErrUnsupported
	case CodeNotFound:
		sentinel = ErrNotFound
	case CodeBusy:
		sentinel = ErrBusy
	case CodeValidation:
		sentinel = ErrValidation
	case CodeDeviceUnavailable:
		sentinel = ErrDeviceUnavailable
	case CodeDenied:
		sentinel = ErrPolicyDenied
	default:
		return errors.New(msg)
	}
	if msg == "" {
		return sentinel
	}
	return &wireError{msg: msg, sentinel: sentinel}
}
