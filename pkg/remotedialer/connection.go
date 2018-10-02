package remotedialer

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rancher/rancher/pkg/metrics"
)

type connection struct {
	sync.Mutex

	err           error
	writeDeadline time.Time
	buf           chan []byte
	readBuf       []byte
	addr          addr
	session       *session
	connID        int64
}

func newConnection(connID int64, session *session, proto, address string) *connection {
	c := &connection{
		addr: addr{
			proto:   proto,
			address: address,
		},
		connID:  connID,
		session: session,
		buf:     make(chan []byte, 1024),
	}
	if metrics.PrometheusMetrics {
		metrics.TotalAddConnectionsForWS.With(
			prometheus.Labels{
				"clientkey": session.clientKey,
				"proto":     proto,
				"addr":      address}).Inc()
	}
	return c
}

func (c *connection) tunnelClose(err error) {
	if metrics.PrometheusMetrics {
		metrics.TotalRemoveConnectionsForWS.With(
			prometheus.Labels{
				"clientkey": c.session.clientKey,
				"proto":     c.addr.Network(),
				"addr":      c.addr.String()}).Inc()
	}
	c.writeErr(err)
	c.doTunnelClose(err)
}

func (c *connection) doTunnelClose(err error) {
	c.Lock()
	defer c.Unlock()

	if c.err != nil {
		return
	}

	c.err = err
	if c.err == nil {
		c.err = io.ErrClosedPipe
	}

	close(c.buf)
}

func (c *connection) tunnelWriter() io.Writer {
	return chanWriter{conn: c, C: c.buf}
}

func (c *connection) Close() error {
	c.session.closeConnection(c.connID, io.EOF)
	return nil
}

func (c *connection) copyData(b []byte) int {
	n := copy(b, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n
}

func (c *connection) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	n := c.copyData(b)
	if n > 0 {
		if metrics.PrometheusMetrics {
			metrics.TotalReceiveBytesOnWS.With(
				prometheus.Labels{
					"clientkey": c.session.clientKey,
				}).Add(float64(n))
		}
		return n, nil
	}

	next, ok := <-c.buf
	if !ok {
		err := io.EOF
		c.Lock()
		if c.err != nil {
			err = c.err
		}
		c.Unlock()
		return 0, err
	}

	c.readBuf = next
	n = c.copyData(b)
	if metrics.PrometheusMetrics {
		metrics.TotalReceiveBytesOnWS.With(
			prometheus.Labels{
				"clientkey": c.session.clientKey,
			}).Add(float64(n))
	}
	return n, nil
}

func (c *connection) Write(b []byte) (int, error) {
	c.Lock()
	if c.err != nil {
		defer c.Unlock()
		return 0, c.err
	}
	c.Unlock()

	deadline := int64(0)
	if !c.writeDeadline.IsZero() {
		deadline = c.writeDeadline.Sub(time.Now()).Nanoseconds() / 1000000
	}
	msg := newMessage(c.connID, deadline, b)
	if metrics.PrometheusMetrics {
		metrics.TotalTransmitBytesOnWS.With(
			prometheus.Labels{
				"clientkey": c.session.clientKey,
			}).Add(float64(len(msg.Bytes())))
	}
	return c.session.writeMessage(msg)
}

func (c *connection) writeErr(err error) {
	if err != nil {
		msg := newErrorMessage(c.connID, err)
		if metrics.PrometheusMetrics {
			metrics.TotalTransmitErrorBytesOnWS.With(
				prometheus.Labels{
					"clientkey": c.session.clientKey,
				}).Add(float64(len(msg.Bytes())))
		}
		c.session.writeMessage(msg)
	}
}

func (c *connection) LocalAddr() net.Addr {
	return c.addr
}

func (c *connection) RemoteAddr() net.Addr {
	return c.addr
}

func (c *connection) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *connection) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *connection) SetWriteDeadline(t time.Time) error {
	c.writeDeadline = t
	return nil
}

type addr struct {
	proto   string
	address string
}

func (a addr) Network() string {
	return a.proto
}

func (a addr) String() string {
	return a.address
}

type chanWriter struct {
	conn *connection
	C    chan []byte
}

func (c chanWriter) Write(buf []byte) (int, error) {
	c.conn.Lock()
	defer c.conn.Unlock()

	if c.conn.err != nil {
		return 0, c.conn.err
	}

	newBuf := make([]byte, len(buf))
	copy(newBuf, buf)
	buf = newBuf

	select {
	// must copy the buffer
	case c.C <- buf:
		return len(buf), nil
	default:
		select {
		case c.C <- buf:
			return len(buf), nil
		case <-time.After(15 * time.Second):
			return 0, errors.New("backed up reader")
		}
	}
}
