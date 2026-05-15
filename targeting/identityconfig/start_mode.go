package identityconfig

import "time"

// StartMode controls how Service.Start handles a failed initial LoadAll.
type StartMode int

const (
	// StartModeFailFast — Service.Start returns the LoadAll error.
	// Caller decides what to do (typically: log and exit).
	StartModeFailFast StartMode = iota

	// StartModeRetry — Service.Start blocks, retrying LoadAll per the
	// RetryConfig, until success, attempts/deadline exhausted, or context
	// cancellation. Exhaustion returns the last error.
	StartModeRetry

	// StartModeBestEffort — Service.Start returns nil even when LoadAll
	// fails. The Service proceeds with an empty snapshot and retries the
	// load on the normal refresh tick. The initial failure is logged at
	// Error level.
	StartModeBestEffort
)

// StartConfig groups initial-load policy options.
type StartConfig struct {
	Mode  StartMode
	Retry RetryConfig // only consulted when Mode == StartModeRetry
}

// BackoffStrategy selects the wait-between-attempts curve for StartModeRetry.
type BackoffStrategy int

const (
	// BackoffConstant — every retry waits RetryConfig.Initial.
	BackoffConstant BackoffStrategy = iota

	// BackoffExponential — each retry doubles the prior wait, capped at
	// RetryConfig.Max.
	BackoffExponential
)

// RetryConfig parameterizes StartModeRetry. Zero-valued fields are
// substituted with safe defaults at Service.Start time:
//
//   - Initial defaults to 1s when <= 0.
//   - Max defaults to Initial when <= 0 or < Initial.
//   - Backoff defaults to BackoffConstant.
//   - MaxAttempts == 0 means "unbounded retries until success or Deadline."
//   - Deadline == 0 means "no overall deadline."
type RetryConfig struct {
	Initial     time.Duration
	Max         time.Duration
	Backoff     BackoffStrategy
	MaxAttempts int
	Deadline    time.Duration
}
