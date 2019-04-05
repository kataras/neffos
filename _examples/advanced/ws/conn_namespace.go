package ws

type nsConn struct {
	*conn
	namespace string
	events    Events
}

func (c *nsConn) Emit(event string, body []byte) {
	c.conn.Write(c.namespace, event, body)
}

func (c *nsConn) Disconnect() error {
	return c.conn.disconnect(c.namespace)
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

func (c *nsConns) Emit(event string, body []byte) {
	for _, s := range c.conns {
		s.Emit(event, body)
	}
}

func (c *nsConns) Disconnect() {
	for _, s := range c.conns {
		s.Disconnect()
	}
}
