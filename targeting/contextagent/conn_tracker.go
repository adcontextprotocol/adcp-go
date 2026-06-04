package contextagent

import (
	"net"
	"sync"
	"sync/atomic"
)

// connTracker counts in-flight TCP connections opened against the
// context-agent listener. Goroutine-safe and zero-value usable.
//
// Pair with trackingListener at the network layer and
// MetricsProvider.RegisterOpenConnectionsObserver at the metrics layer
// so the count is observable through Prometheus.
type connTracker struct {
	open atomic.Int64
}

// Open returns the current open-connection count.
func (t *connTracker) Open() int64 { return t.open.Load() }

// trackingListener wraps a net.Listener and increments the connTracker
// every time Accept succeeds. The conn it returns decrements the
// tracker exactly once when closed, regardless of how many times Close
// is invoked.
type trackingListener struct {
	net.Listener
	tracker *connTracker
}

func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.tracker.open.Add(1)
	return &trackingConn{Conn: c, tracker: l.tracker}, nil
}

// trackingConn decrements the tracker on its first Close. Subsequent
// Close calls forward to the wrapped conn but do not touch the counter —
// net/http calls Close redundantly in some paths and double-decrementing
// would underflow the gauge.
type trackingConn struct {
	net.Conn
	tracker *connTracker
	once    sync.Once
}

func (c *trackingConn) Close() error {
	c.once.Do(func() { c.tracker.open.Add(-1) })
	return c.Conn.Close()
}
