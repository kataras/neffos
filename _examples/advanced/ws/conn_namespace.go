package ws

import (
	"context"
	"sync"
)

type NSConn struct {
	Conn *Conn
	// Static from server, client can select which to use or not.
	// Client and server can ask to connect.
	// Server can forcely disconnect.
	namespace string
	// Static from server, client can select which to use or not.
	events Events

	// Dynamically channels/rooms for each connected namespace.
	// Client can ask to join, server can forcely join a connection to a room.
	// Namespace(room(fire event)).
	rooms   map[string]*Room
	roomsMu sync.RWMutex
}

func newNSConn(c *Conn, namespace string, events Events) *NSConn {
	return &NSConn{
		Conn:      c,
		namespace: namespace,
		events:    events,
		rooms:     make(map[string]*Room),
	}
}

func (ns *NSConn) Emit(event string, body []byte) bool {
	if ns == nil { // if for any reason Namespace() called without be available.
		return false
	}

	return ns.Conn.Write(Message{Namespace: ns.namespace, Event: event, Body: body})
}

func (ns *NSConn) Ask(ctx context.Context, event string, body []byte) (Message, error) {
	if ns == nil {
		return Message{}, ErrWrite
	}

	return ns.Conn.Ask(ctx, Message{Namespace: ns.namespace, Event: event, Body: body})
}

func (ns *NSConn) JoinRoom(ctx context.Context, roomName string) (*Room, error) {
	if ns == nil {
		return nil, ErrWrite
	}

	return ns.askRoomJoin(ctx, roomName)
}

func (ns *NSConn) Room(roomName string) *Room {
	if ns == nil {
		return nil
	}

	ns.roomsMu.RLock()
	room := ns.rooms[roomName]
	ns.roomsMu.RUnlock()

	return room
}

func (ns *NSConn) LeaveAll(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	// TODO:
	return nil
}

func (ns *NSConn) Disconnect(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	return ns.Conn.askDisconnect(ctx, Message{
		Namespace: ns.namespace,
		Event:     OnNamespaceDisconnect,
	}, true)
}

func (ns *NSConn) askRoomJoin(ctx context.Context, roomName string) (*Room, error) {
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

	_, err := ns.Conn.Ask(ctx, joinMessage)
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

func (ns *NSConn) replyRoomJoin(msg Message) {
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

	ns.Conn.Write(msg)
}

func (ns *NSConn) askRoomLeave(ctx context.Context, msg Message) error {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return nil
	}
	// TODO:
	return nil
}

func (ns *NSConn) replyRoomLeave(msg Message) {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return
	}
	// TODO:
}
