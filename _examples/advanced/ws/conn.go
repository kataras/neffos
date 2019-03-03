package ws

import (
	"sync/atomic"
	// "time"

	"github.com/kataras/fastws"
)

type Conn interface {
	UnderlyingConn() *fastws.Conn
	ID() string
	Emit(string, []byte)
	Close()
	String() string
}

type conn struct {
	underline *fastws.Conn
	rooms     map[string]*Room
	ns        map[string]*connOnRoom

	closeCh chan struct{}
	out     chan []byte
	in      chan []byte

	once uint32

	server *Server
}

type connOnRoom struct {
	*conn
	room string
}

func (c *connOnRoom) Emit(event string, body []byte) {
	c.write(c.room, event, body)
}

func newConn(underline *fastws.Conn, rooms map[string]*Room) *conn {
	c := &conn{
		underline: underline,
		rooms:     rooms,
		ns:        make(map[string]*connOnRoom),

		closeCh: make(chan struct{}),
		out:     make(chan []byte, 256),
		// in:      make(chan []byte),
	}

	return c
}

var (
	newline = []byte{'\n'}
)

func (c *conn) startWriter() {
	defer c.Close()

	for {
		select {
		case <-c.closeCh:
			return
		case b, ok := <-c.out:
			if !ok {
				return
			}
			_, err := c.underline.Write(b)
			if err != nil {
				return
			}

			// for i, n := 0, len(c.out); i < n; i++ {
			// 	c.underline.Write(newline)
			// 	c.underline.Write(<-c.out)
			// }
		}
	}
}

func (c *conn) startReader() {
	defer c.Close()

	for {
		b, err := c.underline.ReadBinary()
		if err != nil {
			return
		}

		if err = c.messageReceived(b); err != nil {
			return
		}
	}
}

// func (c *conn) serve() error {
// 	go c.startWriter()
// 	return c.startReader()
// }

// func (c *conn) serve() error {
// 	// go c.startReader()

// 	defer c.Close()

// 	go func() {
// 		for {
// 			select {
// 			case <-c.closeCh:
// 				// close(c.in)
// 				// close(c.out)
// 				return
// 			case msg, ok := <-c.out:
// 				if !ok {
// 					return
// 				}
// 				b := serializeOutput(msg)
// 				_, err := c.underline.Write(b)
// 				if err != nil {
// 					c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 						typ: roomError,
// 						err: err,
// 					})
// 					return
// 				}

// 			}
// 		}
// 	}()

// 	err := c.startReader()
// 	return err
// }

func (c *conn) messageReceived(b []byte) error {
	msg := deserializeInput(b)

	err := c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
		typ: roomMessage,
		msg: msg,
	})

	return err
}

// func (c *conn) startReader() error {
// 	for {
// 		b, err := c.underline.ReadBinary()
// 		if err != nil {
// 			if fastws.IsDisconnected(err) {
// 				return nil
// 			}

// 			// c.rooms[""].handle(c.wrapConnRoom(""), roomAction{
// 			// 	typ: roomError,
// 			// 	err: err,
// 			// })
// 			return err
// 		}

// 		if err = c.messageReceived(b); err != nil {
// 			return err
// 		}

// 		// select {
// 		// case <-c.closeCh:
// 		// 	return nil
// 		// case c.in <- b:
// 		// case <-time.After(500 * time.Millisecond):

// 		// 	return nil
// 		// }

// 		// msg := deserializeInput(b)

// 		// err = c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 		// 	typ: roomMessage,
// 		// 	msg: msg,
// 		// })
// 		// if err != nil {
// 		// 	return err
// 		// }
// 	}
// }

// func (c *conn) startWriter() {
// 	defer c.Close()

// 	for {
// 		select {
// 		case <-c.closeCh:
// 			return
// 		case msg, ok := <-c.out:
// 			if !ok {
// 				return
// 			}
// 			b := serializeOutput(msg)
// 			_, err := c.underline.Write(b)
// 			if err != nil {
// 				c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 					typ: roomError,
// 					err: err,
// 				})
// 			}
// 		}
// 	}
// }

// func (c *conn) write(room, event string, body []byte) {
// 	msg := message{
// 		room:  room,
// 		event: event,
// 		body:  body,
// 	}

// 	b := serializeOutput(msg)
// 	_, err := c.underline.Write(b)
// 	if err != nil {
// 		c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 			typ: roomError,
// 			err: err,
// 		})
// 	}
// }

// func (c *conn) startWriter() {
// 	defer c.Close()

// 	for {
// 		select {
// 		case <-c.closeCh:
// 			return
// 		case msg := <-c.out:
// 			b := serializeOutput(msg)
// 			_, err := c.underline.Write(b)
// 			if err != nil {
// 				c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 					typ: roomError,
// 					err: err,
// 				})
// 			}
// 		}
// 	}
// }

func (c *conn) ID() string {
	return c.underline.ID
}

func (c *conn) String() string {
	return c.underline.String()
}

func (c *conn) UnderlyingConn() *fastws.Conn {
	return c.underline
}

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(&c.once, 0, 1) {
		c.leaveAll()
		go func() {
			if c.server != nil {
				c.server.disconnect <- c
			} else {
				close(c.out)
			}

			close(c.closeCh)
		}()

		c.underline.NetConn.Close()
	}
}

func (c *conn) wrapConnRoom(room string) *connOnRoom {
	conn, ok := c.ns[room]
	if !ok {
		conn = &connOnRoom{
			conn: c,
			room: room,
		}
		c.ns[room] = conn

		c.rooms[room].handle(conn, roomAction{
			typ: roomJoin,
		})
	}

	return conn
}

func (c *conn) leave(room string) {
	c.rooms[room].handle(c.wrapConnRoom(room), roomAction{
		typ: roomLeave,
	})

	delete(c.ns, room)
}

func (c *conn) leaveAll() {
	for room := range c.ns {
		c.leave(room)
	}
}

func (c *conn) write(room, event string, body []byte) {
	select {
	case <-c.closeCh:
		return
	case c.out <- serializeOutput(message{
		room:  room,
		event: event,
		body:  body,
	}):
	}
}

// func (c *conn) startReader() error {
// 	defer c.Close()

// 	for {
// 		b, err := c.underline.ReadBinary()
// 		if err != nil {
// 			if fastws.IsDisconnected(err) {
// 				return nil
// 			}

// 			c.rooms[""].handle(c.wrapConnRoom(""), roomAction{
// 				typ: roomError,
// 				err: err,
// 			})

// 			return err
// 		}
// 		c.in <- b
// 	}
// }

// func (c *conn) read() error {
// 	var (
// 		msg message
// 		err error
// 	)

// 	select {
// 	case b := <-c.in:
// 		msg = deserializeInput(b)
// 		err = c.rooms[msg.room].handle(c.wrapConnRoom(msg.room), roomAction{
// 			typ: roomMessage,
// 			msg: msg,
// 		})
// 		return err
// 	case <-c.closeCh:
// 		return nil
// 	}
// }
