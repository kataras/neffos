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

// Emit method sends a message to the remote side with its `Message.Room` filled to this specific room
// and `Message.Namespace` to the underline `NSConn`'s namespace.
func (r *Room) Emit(event string, body []byte) bool {
	return r.NSConn.Conn.Write(Message{
		Namespace: r.NSConn.namespace,
		Room:      r.Name,
		Event:     event,
		Body:      body,
	})
}

// Leave method sends a remote and local leave room signal `OnRoomLeave` to this specific room
// and fires the `OnRoomLeft` event if succeed.
func (r *Room) Leave(ctx context.Context) error {
	return r.NSConn.askRoomLeave(ctx, Message{
		Namespace: r.NSConn.namespace,
		Room:      r.Name,
		Event:     OnRoomLeave,
	}, true)
}
