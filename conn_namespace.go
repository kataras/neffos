package neffos

import (
	"context"
	"sync"
)

// NSConn describes a connection connected to a specific namespace,
// it emits with the `Message.Namespace` filled and it can join to multiple rooms.
// A single `Conn` can be connected to one or more namespaces,
// each connected namespace is described by this structure.
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
	rooms      map[string]*Room
	roomsMutex sync.RWMutex

	// value is the per-connection *structInstance of a dynamic Struct handler,
	// set on OnNamespaceConnect and read by its events and by `Instance`.
	value any
}

// Instance returns the per-connection instance of a dynamic `Struct[T]` handler
// (one whose T has a `*NSConn` field), created when this connection connected
// to the namespace. It reports false before that, for a static struct, and when
// the handler's type is not T.
//
//	chat, ok := c.Instance[Chat]()
func (ns *NSConn) Instance[T any]() (*T, bool) {
	if ns == nil {
		return nil, false
	}

	inst, ok := ns.value.(*structInstance)
	if !ok {
		return nil, false
	}

	t, ok := inst.ptr.(*T)
	return t, ok
}

func newNSConn(c *Conn, namespace string, events Events) *NSConn {
	return &NSConn{
		Conn:      c,
		namespace: namespace,
		events:    events,
		rooms:     make(map[string]*Room),
	}
}

// String method simply returns the Conn's ID().
// Useful method to this connected to a namespace connection to be passed on `Server#Broadcast` method
// to exclude itself from the broadcasted message's receivers.
func (ns *NSConn) String() string {
	return ns.Conn.String()
}

// Broadcast sends msgs to every connection, including the sender. Equivalent to
// `ns.Conn.Server().Broadcast(nil, msgs...)` but reads more clearly inside an
// event callback. Delivery is at-most-once; see Server.Broadcast for details.
//
// This is a server-side helper: on a client-side NSConn it does nothing.
func (ns *NSConn) Broadcast(msgs ...Message) {
	if ns == nil || ns.Conn.IsClient() {
		return
	}

	ns.Conn.server.Broadcast(nil, msgs...)
}

// BroadcastOthers sends msgs to every connection except this one. Equivalent to
// `ns.Conn.Server().Broadcast(ns.Conn, msgs...)`.
//
// This is a server-side helper: on a client-side NSConn it does nothing.
func (ns *NSConn) BroadcastOthers(msgs ...Message) {
	if ns == nil || ns.Conn.IsClient() {
		return
	}

	ns.Conn.server.Broadcast(ns.Conn, msgs...)
}

// excludeKeys implements Sender for the namespace's connection.
func (ns *NSConn) excludeKeys() (serverConnID, connID string) {
	return ns.Conn.excludeKeys()
}

// Emit method sends a message to the remote side
// with its `Message.Namespace` filled to this specific namespace.
// It is `Send(event, body) == nil`.
func (ns *NSConn) Emit(event string, body []byte) bool {
	return ns.Send(event, body) == nil
}

// Send sends a message to the remote side with its `Message.Namespace`
// filled to this specific namespace, and returns nil once it was handed off
// to the socket. A nil NSConn returns ErrBadNamespace; see `Conn.Send` for
// the other errors.
func (ns *NSConn) Send(event string, body []byte) error {
	return ns.send(Message{Event: event, Body: body})
}

// SendObject is `Send` for a value: it encodes "v" with `Marshal` and sends the
// result as the body. An encoding error is returned and nothing is sent.
//
//	return c.SendObject("chat", chatMessage{From: "makis", Text: "hi"})
func (ns *NSConn) SendObject(event string, v any) error {
	body, err := Marshal(v)
	if err != nil {
		return err
	}

	return ns.Send(event, body)
}

// EmitBinary acts like `Emit` but it sets the `Message.SetBinary` to true
// and sends the data as binary, the receiver's Message in javascript-side is Uint8Array.
func (ns *NSConn) EmitBinary(event string, body []byte) bool {
	return ns.send(Message{Event: event, Body: body, SetBinary: true}) == nil
}

// send fills msg.Namespace and sends msg through the connection.
func (ns *NSConn) send(msg Message) error {
	if ns == nil {
		return ErrBadNamespace
	}

	msg.Namespace = ns.namespace
	return ns.Conn.Send(msg)
}

// Ask method writes a message to the remote side and blocks until a response or an error received.
func (ns *NSConn) Ask(ctx context.Context, event string, body []byte) (Message, error) {
	if ns == nil {
		return Message{}, ErrWrite
	}

	return ns.Conn.Ask(ctx, Message{Namespace: ns.namespace, Event: event, Body: body})
}

// AskObject is `Ask` for values: it encodes "v" with `Marshal`, waits for the
// reply and decodes the reply's body into a Reply with `Message.As`.
//
//	user, err := c.AskObject[User](ctx, "login", credentials)
func (ns *NSConn) AskObject[Reply any](ctx context.Context, event string, v any) (Reply, error) {
	var zero Reply

	body, err := Marshal(v)
	if err != nil {
		return zero, err
	}

	msg, err := ns.Ask(ctx, event, body)
	if err != nil {
		return zero, err
	}

	return msg.As[Reply]()
}

// JoinRoom method can be used to join a connection to a specific room, rooms are dynamic.
// Returns the joined `Room`.
func (ns *NSConn) JoinRoom(ctx context.Context, roomName string) (*Room, error) {
	if ns == nil {
		return nil, ErrWrite
	}

	return ns.askRoomJoin(ctx, roomName)
}

// Room method returns a joined `Room`.
func (ns *NSConn) Room(roomName string) *Room {
	if ns == nil {
		return nil
	}

	ns.roomsMutex.RLock()
	room := ns.rooms[roomName]
	ns.roomsMutex.RUnlock()

	return room
}

// Rooms returns a slice copy of the joined rooms.
func (ns *NSConn) Rooms() []*Room {
	ns.roomsMutex.RLock()
	rooms := make([]*Room, len(ns.rooms))
	i := 0
	for _, room := range ns.rooms {
		rooms[i] = room
		i++
	}
	ns.roomsMutex.RUnlock()

	return rooms
}

// LeaveAll method sends a remote and local leave room signal `OnRoomLeave` to and for all rooms
// and fires the `OnRoomLeft` event if succeed.
func (ns *NSConn) LeaveAll(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	// No lock is held while asking or firing events, so callbacks may read the
	// namespace's rooms. A room left meanwhile is skipped.
	leaveMsg := Message{Namespace: ns.namespace, Event: OnRoomLeave, IsLocal: true}
	for _, room := range ns.snapshotRooms() {
		if ns.Room(room) == nil {
			continue
		}

		leaveMsg.Room = room
		if err := ns.askRoomLeave(ctx, leaveMsg); err != nil {
			return err
		}
	}

	return nil
}

// snapshotRooms returns the names of the joined rooms.
func (ns *NSConn) snapshotRooms() []string {
	ns.roomsMutex.RLock()
	defer ns.roomsMutex.RUnlock()

	names := make([]string, 0, len(ns.rooms))
	for name := range ns.rooms {
		names = append(names, name)
	}
	return names
}

// forceLeaveAll removes every joined room and then fires OnRoomLeave and
// OnRoomLeft for each, without holding roomsMutex.
func (ns *NSConn) forceLeaveAll(isLocal bool) {
	ns.roomsMutex.Lock()
	rooms := make([]string, 0, len(ns.rooms))
	for room := range ns.rooms {
		rooms = append(rooms, room)
	}
	clear(ns.rooms)
	ns.roomsMutex.Unlock()

	leaveMsg := Message{Namespace: ns.namespace, IsForced: true, IsLocal: isLocal}
	for _, room := range rooms {
		leaveMsg.Room = room

		leaveMsg.Event = OnRoomLeave
		ns.events.fireEvent(ns, leaveMsg)

		leaveMsg.Event = OnRoomLeft
		ns.events.fireEvent(ns, leaveMsg)
	}
}

// Disconnect method sends a disconnect signal to the remote side and fires the local `OnNamespaceDisconnect` event.
func (ns *NSConn) Disconnect(ctx context.Context) error {
	if ns == nil {
		return nil
	}

	return ns.Conn.askDisconnect(ctx, Message{
		Namespace: ns.namespace,
		Event:     OnNamespaceDisconnect,
	})
}

func (ns *NSConn) askRoomJoin(ctx context.Context, roomName string) (*Room, error) {
	ns.roomsMutex.RLock()
	room, ok := ns.rooms[roomName]
	ns.roomsMutex.RUnlock()
	if ok {
		return room, nil
	}

	joinMsg := Message{
		Namespace: ns.namespace,
		Room:      roomName,
		Event:     OnRoomJoin,
		IsLocal:   true,
	}

	_, err := ns.Conn.Ask(ctx, joinMsg)
	if err != nil {
		return nil, err
	}

	err = ns.events.fireEvent(ns, joinMsg)
	if err != nil {
		return nil, err
	}

	room = newRoom(ns, roomName)
	ns.roomsMutex.Lock()
	ns.rooms[roomName] = room
	ns.roomsMutex.Unlock()

	joinMsg.Event = OnRoomJoined
	ns.events.fireEvent(ns, joinMsg)
	return room, nil
}

func (ns *NSConn) replyRoomJoin(msg Message) {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return
	}

	ns.roomsMutex.RLock()
	_, ok := ns.rooms[msg.Room]
	ns.roomsMutex.RUnlock()
	if !ok {
		err := ns.events.fireEvent(ns, msg)
		if err != nil {
			ns.Conn.replyError(msg, err)
			return
		}
		ns.roomsMutex.Lock()
		ns.rooms[msg.Room] = newRoom(ns, msg.Room)
		ns.roomsMutex.Unlock()

		msg.Event = OnRoomJoined
		ns.events.fireEvent(ns, msg)
	}

	ns.Conn.writeEmptyReply(msg.wait)
}

func (ns *NSConn) askRoomLeave(ctx context.Context, msg Message) error {
	if ns == nil {
		return nil
	}

	if ns.Room(msg.Room) == nil {
		return ErrBadRoom
	}

	_, err := ns.Conn.Ask(ctx, msg)
	if err != nil {
		return err
	}

	// msg.IsLocal = true
	err = ns.events.fireEvent(ns, msg)
	if err != nil {
		return err
	}

	ns.roomsMutex.Lock()
	delete(ns.rooms, msg.Room)
	ns.roomsMutex.Unlock()

	msg.Event = OnRoomLeft
	ns.events.fireEvent(ns, msg)

	return nil
}

func (ns *NSConn) replyRoomLeave(msg Message) {
	if ns == nil || msg.wait == "" || msg.isNoOp {
		return
	}

	room := ns.Room(msg.Room)
	if room == nil {
		ns.Conn.writeEmptyReply(msg.wait)
		return
	}

	// if client then we need to respond to server and delete the room without ask the local event.
	if ns.Conn.IsClient() {
		ns.events.fireEvent(ns, msg)

		ns.roomsMutex.Lock()
		delete(ns.rooms, msg.Room)
		ns.roomsMutex.Unlock()

		ns.Conn.writeEmptyReply(msg.wait)

		msg.Event = OnRoomLeft
		ns.events.fireEvent(ns, msg)
		return
	}

	// server-side, check for error on the local event first.
	err := ns.events.fireEvent(ns, msg)
	if err != nil {
		ns.Conn.replyError(msg, err)
		return
	}

	ns.roomsMutex.Lock()
	delete(ns.rooms, msg.Room)
	ns.roomsMutex.Unlock()

	msg.Event = OnRoomLeft
	ns.events.fireEvent(ns, msg)

	ns.Conn.writeEmptyReply(msg.wait)
}
