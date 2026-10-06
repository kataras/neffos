package neffos

import (
	"context"
)

// Room describes a connected connection to a room,
// emits messages with the `Message.Room` filled to the specific room
// and `Message.Namespace` to the underline `NSConn`'s namespace.
type Room struct {
	NSConn *NSConn

	Name string
}

func newRoom(ns *NSConn, roomName string) *Room {
	return &Room{
		NSConn: ns,
		Name:   roomName,
	}
}

// String method simply returns the Conn's ID().
// To get the room's name simply use the `Room.Name` struct field instead.
// Useful method to this room to be passed on `Server#Broadcast` method
// to exclude itself from the broadcasted message's receivers.
func (r *Room) String() string {
	return r.NSConn.String()
}

// Emit method sends a message to the remote side with its `Message.Room` filled to this specific room
// and `Message.Namespace` to the underline `NSConn`'s namespace.
// It is `Send(event, body) == nil`.
func (r *Room) Emit(event string, body []byte) bool {
	return r.Send(event, body) == nil
}

// Send sends a message to the remote side with its `Message.Room` filled to
// this specific room and `Message.Namespace` to the underline `NSConn`'s
// namespace, and returns nil once it was handed off to the socket. A nil Room
// returns ErrBadRoom; see `Conn.Send` for the other errors.
func (r *Room) Send(event string, body []byte) error {
	if r == nil {
		return ErrBadRoom
	}

	return r.NSConn.send(Message{
		Room:  r.Name,
		Event: event,
		Body:  body,
	})
}

// Leave method sends a remote and local leave room signal `OnRoomLeave` to this specific room
// and fires the `OnRoomLeft` event if succeed.
func (r *Room) Leave(ctx context.Context) error {
	return r.NSConn.askRoomLeave(ctx, Message{
		Namespace: r.NSConn.namespace,
		Room:      r.Name,
		Event:     OnRoomLeave,
	})
}
