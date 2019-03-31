package ws

type nsConn struct {
	*conn
	namespace string
	events    Events
}

func (c *nsConn) Emit(event string, body []byte) {
	c.Write(c.namespace, event, body, nil)
}

func (c *nsConn) Disconnect() {
	c.conn.disconnect(c.namespace)
}

func newNSConn(c *conn, namespace string, events Events) *nsConn {
	return &nsConn{
		conn:      c,
		namespace: namespace,
		events:    events,
	}
}
