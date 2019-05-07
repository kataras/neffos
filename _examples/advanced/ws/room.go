package ws

type room struct {
	*nsConn

	name string
}

func newRoom(ns *nsConn, roomName string) *room {
	return &room{
		nsConn: ns,
		name:   roomName,
	}
}

func (r *room) NSConn() NSConn {
	return r.nsConn
}

func (r *room) Emit(event string, body []byte) bool {
	return r.nsConn.conn.Write(Message{
		Namespace: r.nsConn.namespace,
		Room:      r.name,
		Event:     event,
		Body:      body,
	})
}

func (r *room) Leave() error {
	return r.nsConn.askRoomLeave(Message{
		Namespace: r.nsConn.namespace,
		Room:      r.name,
		Event:     OnRoomLeave,
	})
}
