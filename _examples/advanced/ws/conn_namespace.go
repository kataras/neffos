package ws

import (
	"context"
)

type nsConn struct {
	*conn
	namespace string
	events    Events
}

func (c *nsConn) Emit(event string, body []byte) bool {
	return c.conn.Write(c.namespace, event, body)
}

func (c *nsConn) Ask(ctx context.Context, event string, body []byte) Message {
	return c.conn.WriteAndWait(ctx, c.namespace, event, body)
}

func (c *nsConn) Disconnect(ctx context.Context) error {
	return c.conn.DisconnectFrom(ctx, c.namespace)
}

func newNSConn(c *conn, namespace string, events Events) *nsConn {
	return &nsConn{
		conn:      c,
		namespace: namespace,
		events:    events,
	}
}

type nsConns struct {
	conns []*nsConn
}

func (c *nsConns) Emit(event string, body []byte) bool {
	ok := true
	for _, s := range c.conns {
		if !s.Emit(event, body) {
			ok = false
		}
	}
	return ok
}

func (c *nsConns) Disconnect() {
	for _, s := range c.conns {
		s.Disconnect(nil)
	}
}
