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
	rooms   map[string]*room
	roomsMu sync.RWMutex
}

func newNSConn(c *conn, namespace string, events Events) *nsConn {
	return &nsConn{
		conn:      c,
		namespace: namespace,
		events:    events,
		rooms:     make(map[string]*room),
	}
}

func (ns *nsConn) Conn() Conn {
	return ns.conn
}

func (ns *nsConn) Emit(event string, body []byte) bool {
	if ns == nil { // if for any reason Namespace() called without be available.
		return false
	}

	return ns.conn.Write(Message{Namespace: ns.namespace, Event: event, Body: body})
}

func (ns *nsConn) Ask(ctx context.Context, event string, body []byte) (Message, error) {
	if ns == nil {
		return Message{}, ErrWrite
	}

	return ns.conn.Ask(ctx, Message{Namespace: ns.namespace, Event: event, Body: body})
}

func (ns *nsConn) JoinRoom(ctx context.Context, roomName string) (Room, error) {
	if ns == nil {
		return nil, ErrWrite
	}

	return ns.askRoomJoin(ctx, roomName)
}

func (ns *nsConn) Room(roomName string) Room {
	if ns == nil {
		return nil
	}

	ns.roomsMu.RLock()
	room := ns.rooms[roomName]
	ns.roomsMu.RUnlock()

	return room
}

func (ns *nsConn) LeaveAll(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	// TODO:
	return nil
}

func (ns *nsConn) Disconnect(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	return ns.conn.askDisconnect(ctx, Message{
		Namespace: ns.namespace,
		Event:     OnNamespaceDisconnect,
	}, true)
}

func (ns *nsConn) askRoomJoin(ctx context.Context, roomName string) (*room, error) {
	ns.roomsMu.RLock()
	room, ok := ns.rooms[roomName]
	ns.roomsMu.RUnlock()
	if ok {
		return room, nil
	}

	joinMessage := Message{
		Namespace: ns.namespace,
		Room:      roomName,
		Event:     OnRoomJoin,
		IsLocal:   true,
	}

	_, err := ns.conn.Ask(ctx, joinMessage)
	if err != nil {
		return nil, err
	}

	err = ns.events.fireEvent(ns, joinMessage)
	if err != nil {
		return nil, err
	}

	ns.roomsMu.Lock()
	ns.rooms[roomName] = newRoom(ns, roomName)
	ns.roomsMu.Unlock()

	joinMessage.Event = OnRoomJoined
	ns.events.fireEvent(ns, joinMessage)
	return room, nil
}

func (ns *nsConn) replyRoomJoin(msg Message) {
	if ns == nil || msg.wait == "" || msg.isNoOp {
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
			ns.rooms[msg.Room] = newRoom(ns, msg.Room)
			ns.roomsMu.Unlock()

			msg.Event = OnRoomJoined
			ns.events.fireEvent(ns, msg)
		}
	}

	ns.conn.Write(msg)
}

func (ns *nsConn) askRoomLeave(msg Message) error {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return nil
	}
	// TODO:
	return nil
}

func (ns *nsConn) replyRoomLeave(msg Message) {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return
	}
	// TODO:
}
