package identityagent

import (
	"net"
	"sync"
	"sync/atomic"
)

// connTracker counts in-flight TCP connections opened against the
// identity-agent listener. It is goroutine-safe and zero-value usable.
//
// Pair with trackingListener at the network layer and an
// MetricsProvider.RegisterOpenConnectionsObserver call at the metrics
// layer so the open count is observable through Prometheus.
type connTracker struct {
	open atomic.Int64
}

// Open returns the current open-connection count.
func (t *connTracker) Open() int64 { return t.open.Load() }

// trackingListener wraps a net.Listener and increments the connTracker
// every time Accept succeeds. The conn it returns decrements the tracker
// exactly once when closed, regardless of how many times Close is invoked.
//
// Compose underneath netutil.LimitListener so the cap and the tracker
// share the same accept event:
//
//	ln, _ := net.Listen("tcp", addr)
//	ln = netutil.LimitListener(ln, maxConns)
//	ln = &trackingListener{Listener: ln, tracker: tracker}
//	srv.Serve(ln)
type trackingListener struct {
	net.Listener
	tracker *connTracker
}

// Accept delegates to the underlying listener and registers the connection
// with the tracker on success.
func (l *trackingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.tracker.open.Add(1)
	return &trackingConn{Conn: c, tracker: l.tracker}, nil
}

// trackingConn decrements the tracker on its first Close. Subsequent
// Close calls are forwarded to the wrapped conn but do not touch the
// counter — Go's net stack and net/http both call Close redundantly in
// some paths (server keep-alive teardown plus an explicit close from a
// handler, for example) and counting them would double-decrement.
type trackingConn struct {
	net.Conn
	tracker *connTracker
	once    sync.Once
}

func (c *trackingConn) Close() error {
	c.once.Do(func() { c.tracker.open.Add(-1) })
	return c.Conn.Close()
}
