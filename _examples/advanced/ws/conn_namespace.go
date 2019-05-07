package ws

import (
	"context"
	"sync"
)

type connectedNamespaces struct {
	sync.RWMutex
	namespaces map[string]*nsConn
}

func (n *connectedNamespaces) add(ns *nsConn) {
	n.Lock()
	n.namespaces[ns.namespace] = ns
	n.Unlock()
}

func (n *connectedNamespaces) remove(namespace string, lock bool) bool {
	if lock {
		n.Lock()
		defer n.Unlock()
	}
	l := len(n.namespaces)
	delete(n.namespaces, namespace)
	return l > len(n.namespaces)
}

func (n *connectedNamespaces) get(namespace string) *nsConn {
	n.RLock()
	ns := n.namespaces[namespace]
	n.RUnlock()

	return ns
}

func (n *connectedNamespaces) exists(namespace string) bool {
	return n.get(namespace) != nil
}

func (n *connectedNamespaces) askConnect(ctx context.Context, c *conn, namespace string) (*nsConn, error) {
	ns := n.get(namespace)
	if ns != nil {
		return ns, nil
	}

	events, ok := c.namespaces[namespace]
	if !ok {
		return nil, ErrBadNamespace
	}

	connectMessage := Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		IsLocal:   true,
	}

	_, err := c.ask(ctx, connectMessage) // waits for answer no matter if already connected on the other side.
	if err != nil {
		return nil, err
	}

	// re-check, maybe local connected.
	ns = n.get(namespace)
	if ns != nil {
		return ns, nil
	}

	ns = newNSConn(c, namespace, events)
	err = events.fireEvent(ns, connectMessage)
	if err != nil {
		return nil, err
	}

	n.add(ns)

	connectMessage.Event = OnNamespaceConnected
	events.fireEvent(ns, connectMessage) // omit error, it's connected.
	return ns, nil
}

func (n *connectedNamespaces) replyConnect(c *conn, msg Message) {
	// must give answer even a noOp if already connected.
	if msg.wait == "" || msg.isNoOp {
		return
	}

	if n.exists(msg.Namespace) {
		msg.isNoOp = true
	} else {
		events, ok := c.namespaces[msg.Namespace]
		if !ok {
			msg.Err = ErrBadNamespace
		} else {
			ns := newNSConn(c, msg.Namespace, events)
			err := events.fireEvent(ns, msg)
			if err != nil {
				msg.Err = err
			} else {
				n.add(ns)
				msg.Event = OnNamespaceConnected
				events.fireEvent(ns, msg)
			}
		}
	}

	c.Write(msg)
}

func (n *connectedNamespaces) forceDisconnectAll() {
	n.RLock()
	disconnectMsg := Message{Event: OnNamespaceDisconnect, IsForced: true, IsLocal: true}
	for _, ns := range n.namespaces {
		disconnectMsg.Namespace = ns.namespace
		ns.events.fireEvent(ns, disconnectMsg)
	}
	n.RUnlock()
}

func (n *connectedNamespaces) disconnectAll(ctx context.Context, c *conn) error {
	c.waitingMessagesMutex.Lock()
	defer c.waitingMessagesMutex.Unlock()

	disconnectMsg := Message{Event: OnNamespaceDisconnect}
	for namespace := range c.connectedNamespaces.namespaces {
		disconnectMsg.Namespace = namespace
		if err := n.askDisconnect(ctx, c, disconnectMsg, false); err != nil {
			return err
		}
	}

	return nil
}

func (n *connectedNamespaces) askDisconnect(ctx context.Context, c *conn, msg Message, lock bool) error {
	var ns *nsConn
	if lock {
		ns = n.get(msg.Namespace)
	} else {
		ns = n.namespaces[msg.Namespace]
	}

	if ns == nil {
		return ErrBadNamespace
	}

	reply, err := c.ask(ctx, msg)
	if err != nil {
		return err
	}

	n.remove(msg.Namespace, lock)

	reply.IsLocal = true
	ns.events.fireEvent(ns, reply)

	return nil
}

func (n *connectedNamespaces) replyDisconnect(c *conn, msg Message) {
	if msg.wait == "" || msg.isNoOp {
		return
	}

	ns := n.get(msg.Namespace)
	if ns == nil {
		return
	}

	// if client then we need to respond to server and delete the namespace without ask the local event.
	if c.IsClient() {
		c.Write(msg)
		n.remove(msg.Namespace, true)
		ns.events.fireEvent(ns, msg)
		return
	}

	// server-side, check for error on the local event first.
	err := ns.events.fireEvent(ns, msg)
	if err != nil {
		msg.Err = err
	} else {
		n.remove(msg.Namespace, true)
	}
	c.Write(msg)
}
