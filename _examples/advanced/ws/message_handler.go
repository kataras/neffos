package ws

var messageHandlers = []func(c *conn, msg Message) (bool, error){
	handleError,
	handleConnect,
	handleMessageEvent,
}

func handleConnect(c *conn, msg Message) (bool, error) {
	if !msg.isConnect {
		return false, nil
	}

	if msg.isNoOp {
		// println("connect: noOp")
		return true, nil
	}

	c.mu.RLock()
	_, alreadyConnected := c.connectedNamespaces[msg.Namespace]
	c.mu.RUnlock()

	if alreadyConnected { // it's already connected, do nothing on the other side.
		// println("incoming connect message but already connected")
		msg.isNoOp = true
		c.write(msg)
		return true, nil
	}

	events, ok := c.namespaces[msg.Namespace] // namespace does not exist, do nothing on the other side.
	if !ok {
		msg.Err = ErrBadNamespace
		c.write(msg)
		return true, nil
	}

	ns := newNSConn(c, msg.Namespace, events)
	err := events.fireEvent(ns, msg)
	if err != nil {
		return true, err
	}

	c.addNSConn(ns)
	msg.isNoOp = true
	msg.Event = OnNamespaceConnected
	events.fireEvent(ns, msg)
	c.write(msg)
	// _, ok = c.ask(msg)
	// if !ok {
	// 	return true, ErrWrite
	// }

	return true, nil
	// msg.Event = OnNamespaceConnected
	// return true, events.fireEvent(ns, msg)
}

func handleError(c *conn, msg Message) (bool, error) {
	if !msg.isError {
		return false, nil
	}

	if msg.Event == "" { // global error, possible server-side.
		c.underline.HandleError(msg.Err)
		return true, nil
	}

	c.mu.RLock()
	ns, ok := c.connectedNamespaces[msg.Namespace]
	c.mu.RUnlock()
	if !ok {
		return true, nil
	}

	return true, ns.events.fireEvent(ns, msg)
}

func handleMessageEvent(c *conn, msg Message) (bool, error) {
	c.mu.RLock()
	ns, ok := c.connectedNamespaces[msg.Namespace]
	c.mu.RUnlock()
	if !ok {
		// see client.go#Connect for the client-side.
		return true, nil
	}

	err := ns.events.fireEvent(ns, msg)
	if err == nil && msg.isDisconnect {
		if !c.IsClient() {
			// send back the disconnect message without error.
			c.write(msg)
		}

		// println("delete " + msg.Namespace)
		c.deleteNSConn(msg.Namespace, true)
	}
	return true, err
}
