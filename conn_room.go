package ws

import (
	"context"
)

type Room struct {
	NSConn *NSConn

	name string
}

func newRoom(ns *NSConn, roomName string) *Room {
	return &Room{
		NSConn: ns,
		name:   roomName,
	}
}

func (r *Room) Emit(event string, body []byte) bool {
	return r.NSConn.Conn.Write(Message{
		Namespace: r.NSConn.namespace,
		Room:      r.name,
		Event:     event,
		Body:      body,
	})
}

func (r *Room) Leave(ctx context.Context) error {
	return r.NSConn.askRoomLeave(ctx, Message{
		Namespace: r.NSConn.namespace,
		Room:      r.name,
		Event:     OnRoomLeave,
	}, true)
}
