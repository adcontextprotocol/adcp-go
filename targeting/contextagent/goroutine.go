package contextagent

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// safeGo runs fn in a new goroutine with deferred panic recovery. A
// panic in fn is logged at ERROR via logger with the stack trace,
// recorded on recorder.BackgroundPanic with the supplied label, and
// swallowed — preventing one background subsystem's panic from crashing
// the agent.
//
// Use this wrapper for every long-lived goroutine the agent launches
// outside the request path (request-path panics are caught by
// recoverMiddleware). where is a short label identifying the subsystem
// for the metric and log (e.g. "keystore-refresh", "http-server").
func safeGo(logger *slog.Logger, recorder Recorder, where string, fn func()) {
	safeGoWithPanicSink(logger, recorder, where, nil, fn)
}

// safeGoWithPanicSink is safeGo extended with an onPanic callback. The
// callback fires after the panic is logged + recorded, and receives a
// synthetic error wrapping the recovered value so the caller can surface
// it through an error channel (e.g. tearing down the agent when the
// HTTP Serve goroutine panics — a black hole otherwise because Serve
// returning means there's no listener and /live cannot tell). A nil
// onPanic behaves identically to plain safeGo.
//
// The synthetic error wraps the recovered value with %w (after coercing
// non-error panics to errors) so callers can `errors.Is` / `errors.As`
// the recovered value back out of the lifecycle error chain.
func safeGoWithPanicSink(logger *slog.Logger, recorder Recorder, where string, onPanic func(error), fn func()) {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = noopRecorder{}
	}
	go func() {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			logger.Error("background goroutine panicked",
				"where", where,
				"error", fmt.Sprintf("%v", rec),
				"stack", string(debug.Stack()),
			)
			recorder.BackgroundPanic(context.Background(), where)
			if onPanic != nil {
				onPanic(fmt.Errorf("panic in %s: %w", where, panicAsError(rec)))
			}
		}()
		fn()
	}()
}

// panicAsError coerces a recovered panic value to an error so it can
// be %w-wrapped. Errors pass through unchanged; everything else
// (strings, integers, struct values) becomes an error whose Error()
// text is fmt's default %v formatting of the value.
func panicAsError(rec any) error {
	if err, ok := rec.(error); ok {
		return err
	}
	return fmt.Errorf("%v", rec)
}
