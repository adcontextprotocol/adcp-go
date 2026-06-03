package identityagent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterConfigEntriesObserver_DisabledProviderIsNoop(t *testing.T) {
	p, err := Build(MetricsConfig{Enabled: false})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	// Must not error and must not panic with a nil meter provider.
	require.NoError(t, p.RegisterConfigEntriesObserver(func() int64 { return 42 }))
}

func TestRegisterConfigEntriesObserver_PublishesGauge(t *testing.T) {
	p, err := Build(MetricsConfig{Enabled: true, Namespace: "identity_agent"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	require.NoError(t, p.RegisterConfigEntriesObserver(func() int64 { return 7 }))

	mfs, err := p.Registry.Gather()
	require.NoError(t, err)

	var found bool
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), "_config_entries") {
			continue
		}
		found = true
		require.Len(t, mf.GetMetric(), 1)
		assert.Equal(t, float64(7), mf.GetMetric()[0].GetGauge().GetValue(),
			"observer callback value should be reported on the gauge at scrape time")
		break
	}
	require.True(t, found, "identity_agent_config_entries gauge should be registered with the Prometheus registry")
}
