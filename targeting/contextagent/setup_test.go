package contextagent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckSuppressionLiveness drives the two thresholds (consecutive
// failures and absolute snapshot age) independently so a regression
// flipping the operator's /live signal to "healthy on a frozen
// snapshot" fails the test.
func TestCheckSuppressionLiveness(t *testing.T) {
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Minute)
	old := now.Add(-(SuppressionStaleMaxAge + time.Second))

	t.Run("healthy_within_thresholds", func(t *testing.T) {
		require.NoError(t, checkSuppressionLiveness(0, fresh, now))
		require.NoError(t, checkSuppressionLiveness(SuppressionStaleThreshold-1, fresh, now))
	})

	t.Run("consecutive_failures_trips", func(t *testing.T) {
		err := checkSuppressionLiveness(SuppressionStaleThreshold, fresh, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "consecutive attempts")
	})

	t.Run("age_trips_independently_of_failure_count", func(t *testing.T) {
		// failures=0 but lastRefresh older than the max age: the
		// refresh-loop has gone silent without incrementing the
		// counter (the bug class this branch exists to catch).
		err := checkSuppressionLiveness(0, old, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ago")
	})

	t.Run("zero_lastRefresh_does_not_trigger_age_branch", func(t *testing.T) {
		// Snapshot constructed but Start/Load never called: keep
		// the age check silent so /live reports healthy until
		// Start runs its synchronous initial Load (or the failure
		// counter trips). The healthy-on-unloaded report is OK
		// because Start is the only legitimate path to running the
		// agent and it always Loads before returning nil.
		require.NoError(t, checkSuppressionLiveness(0, time.Time{}, now))
	})

	t.Run("failure_threshold_beats_age", func(t *testing.T) {
		// Both tripped: the consecutive-failures message has more
		// operator value (tells them Valkey is actively unreachable
		// vs just stale), so the predicate returns it first.
		err := checkSuppressionLiveness(SuppressionStaleThreshold, old, now)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "consecutive attempts")
	})
}
