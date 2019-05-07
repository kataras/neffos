package ws

import (
	"context"
	"sync"
)

type nsConn struct {
	*conn
	// Static from server, client can select which to use or not.
	// Client and server can ask to connect.
	// Server can forcely disconnect.
	namespace string
	// Static from server, client can select which to use or not.
	events Events

	// Dynamically channels/rooms for each connected namespace.
	// Client can ask to join, server can forcely join a connection to a room.
	// Namespace(room(fire event)).
	rooms   map[string]struct{}
	roomsMu sync.RWMutex
}

func newNSConn(c *conn, namespace string, events Events) *nsConn {
	return &nsConn{
		conn:      c,
		namespace: namespace,
		events:    events,
		rooms:     make(map[string]struct{}),
	}
}

func (ns *nsConn) Emit(event string, body []byte) bool {
	return ns.conn.Write(ns.namespace, event, body)
}

func (ns *nsConn) Ask(ctx context.Context, event string, body []byte) Message {
	return ns.conn.WriteAndWait(ctx, ns.namespace, event, body)
}

func (ns *nsConn) Disconnect(ctx context.Context) error {
	return ns.conn.DisconnectFrom(ctx, ns.namespace)
}

func (ns *nsConn) askJoinRoom(ctx context.Context, roomName string) error {
	ns.roomsMu.RLock()
	_, ok := ns.rooms[roomName]
	ns.roomsMu.RUnlock()
	if ok {
		return nil
	}

	joinMessage := Message{
		Namespace: ns.namespace,
		Room:      roomName,
		Event:     OnRoomJoin,
		IsLocal:   true,
	}

	_, err := ns.conn.ask(ctx, joinMessage)
	if err != nil {
		return err
	}

	err = ns.events.fireEvent(ns, joinMessage)
	if err != nil {
		return err
	}

	ns.roomsMu.Lock()
	ns.rooms[roomName] = struct{}{}
	ns.roomsMu.Unlock()

	return nil
}

func (ns *nsConn) replyRoomJoin(msg Message) {
	if msg.wait == "" || msg.isNoOp {
		return
	}

	ns.roomsMu.RLock()
	_, ok := ns.rooms[msg.Room]
	ns.roomsMu.RUnlock()
	if ok {
		msg.isNoOp = true
	} else {
		err := ns.events.fireEvent(ns, msg)
		if err != nil {
			msg.Err = err
		} else {
			ns.roomsMu.Lock()
			ns.rooms[msg.Room] = struct{}{}
			ns.roomsMu.Unlock()
		}
	}

	ns.conn.write(msg)
}

func (ns *nsConn) askRoomLeave(msg Message) {

}

func (ns *nsConn) replyRoomLeave(msg Message) {

}

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

	c.write(msg)
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
	c.mu.Lock()
	defer c.mu.Unlock()

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
		c.write(msg)
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
	c.write(msg)
}
