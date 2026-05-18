package identityagent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// shutdownTask is a single named shutdown step. Fn must respect ctx and be
// idempotent — Cancel may run it under a tight overall deadline.
type shutdownTask struct {
	name string
	fn   func(ctx context.Context) error
}

// shutdownRegistry runs a set of named shutdown tasks sequentially. A panic
// in one task is captured, reported via the recorder, joined into the
// returned error, and does not stop remaining tasks from running. Tasks run
// in registration order.
type shutdownRegistry struct {
	tasks    []shutdownTask
	logger   *slog.Logger
	recorder Recorder
}

func newShutdownRegistry(logger *slog.Logger, recorder Recorder) *shutdownRegistry {
	if logger == nil {
		logger = slog.Default()
	}
	if recorder == nil {
		recorder = noopRecorder{}
	}
	return &shutdownRegistry{logger: logger, recorder: recorder}
}

func (r *shutdownRegistry) add(name string, fn func(ctx context.Context) error) {
	r.tasks = append(r.tasks, shutdownTask{name: name, fn: fn})
}

func (r *shutdownRegistry) cancel(ctx context.Context) error {
	var allErrors error
	for _, t := range r.tasks {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					err := fmt.Errorf("panic in shutdown task %s: %v", t.name, rec)
					r.logger.Error("shutdown task panicked", "task", t.name, "err", err)
					r.recorder.ShutdownPanic(ctx)
					allErrors = errors.Join(allErrors, err)
				}
			}()
			r.logger.Info("cancelling task", "task", t.name)
			if err := t.fn(ctx); err != nil {
				r.logger.Error("could not cancel task", "task", t.name, "err", err)
				allErrors = errors.Join(allErrors, err)
			} else {
				r.logger.Info("cancelled task", "task", t.name)
			}
		}()
	}
	return allErrors
}
