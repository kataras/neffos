package ws

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/fastws"
)

type Conn interface {
	UnderlyingConn() *fastws.Conn
	ID() string
	Write(namespace string, event string, body []byte) bool
	WriteAndWait(namespace, event string, body []byte) Message
	Close()
	String() string
	Connect(namespace string) (NSConn, error)
	DisconnectFrom(namespace string) error
	DisconnectFromAll()

	IsClient() bool
	Server() *Server
}

type NSConn interface {
	Conn
	Emit(event string, body []byte) bool
	Ask(event string, body []byte) Message
	Disconnect() error
}

type conn struct {
	underline  *fastws.Conn
	namespaces Namespaces

	connectedNamespaces map[string]*nsConn
	mu                  sync.RWMutex

	closeCh chan struct{}
	out     chan []byte
	once    *uint32

	waitingMessages map[uint64]chan Message
	server          *Server
}

func newConn(underline *fastws.Conn, namespaces Namespaces) *conn {
	c := &conn{
		underline:           underline,
		namespaces:          namespaces,
		connectedNamespaces: make(map[string]*nsConn),
		closeCh:             make(chan struct{}),
		out:                 make(chan []byte, 256),
		once:                new(uint32),
		waitingMessages:     make(map[uint64]chan Message),
	}

	return c
}

func (c *conn) IsClient() bool {
	return c.server == nil
}

func (c *conn) Server() *Server {
	if c.IsClient() {
		return nil
	}

	return c.server
}

var ackBinary = []byte("ACK")

// this should run before startWriter and startReader.
func (c *conn) ack() error {
	if !c.IsClient() {
		// server-side, first action is to send the id and then wait for confirmation.
		_, err := c.underline.Write([]byte(c.underline.ID))
		if err != nil {
			return err
		}
		// println("server: wrote")

		b, err := c.underline.ReadBinary()
		// println("server: read")
		if err != nil {
			return err
		}

		if bytes.Equal(b, ackBinary) {
			// println("server: ack got")
			return nil
		}
		return CloseError{Code: -1}
	}

	// client-side, first action is to wait for the id and then send the confirmation.
	id, err := c.underline.ReadBinary()
	if err != nil {
		return err
	}
	// println("client: read")

	c.underline.ID = string(id)
	_, err = c.underline.Write(ackBinary)
	// println("client: write")
	return err

	// opposite:
	// if c.IsClient() {
	// 	// server-side, first action is to send the id and then wait for confirmation.
	// 	_, err := c.underline.Write(ackBinary)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	// println("server: wrote")

	// 	id, err := c.underline.ReadBinary()
	// 	// println("server: read")
	// 	if err != nil {
	// 		return err
	// 	}

	// 	c.underline.ID = string(id)
	// 	return nil
	// }

	// // client-side, first action is to wait for the id and then send the confirmation.
	// b, err := c.underline.ReadBinary()
	// if err != nil {
	// 	return err
	// }

	// if bytes.Equal(b, ackBinary) {
	// 	_, err = c.underline.Write([]byte(c.ID()))
	// 	return err
	// }

	// return CloseError{Code: -1}
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
				c.underline.HandleError(err)
				return
			}

			for i, n := 0, len(c.out); i < n; i++ {
				c.underline.Write(newline)
				c.underline.Write(<-c.out)
			}
		}
	}
}

type CloseError struct {
	error
	Code int
}

func (err CloseError) Error() string {
	return fmt.Sprintf("[%d] %s", err.Code, err.error.Error())
}

func isCloseError(err error) bool {
	if err == nil {
		return false
	}

	_, ok := err.(CloseError)
	return ok
}

func (c *conn) startReader() {
	defer c.Close()

readLoop:
	for {
		b, err := c.underline.ReadBinary()
		if err != nil {
			// println("DEBUG - ERROR conn.go: " + err.Error())
			return
		}

		msg := deserializeMessage(nil, b)
		//	fmt.Printf("=============\n%s\n%#+v\n=============\n", string(b), msg)

		if msg.wait > 0 {
			if ch, ok := c.waitingMessages[msg.wait]; ok {
				// fmt.Printf("msg wait: %d for event: %s | isDisconnect: %v\n", msg.wait, msg.Event, msg.isDisconnect)
				ch <- msg
				continue
			}
		}

		for _, h := range messageHandlers {
			handled, err := h(c, msg)
			if err != nil {
				msg.Body = nil
				msg.Err = err
				c.write(msg)
				if isCloseError(err) {
					return // close the connection.
				}
				continue readLoop
			}

			if handled {
				break
			}
		}
	}
}

func (c *conn) ID() string {
	return c.underline.ID
}

func (c *conn) String() string {
	return c.underline.String()
}

func (c *conn) UnderlyingConn() *fastws.Conn {
	return c.underline
}

func (c *conn) addNSConn(ns *nsConn) {
	c.mu.Lock()
	c.connectedNamespaces[ns.namespace] = ns
	c.mu.Unlock()
}

func (c *conn) deleteNSConn(namespace string, lock bool) {
	if lock {
		c.mu.Lock()
		delete(c.connectedNamespaces, namespace)
		c.mu.Unlock()
		return
	}

	delete(c.connectedNamespaces, namespace)
}

func (c *conn) ask(msg Message) (Message, bool) {
	var d uint64 = 1
	if c.IsClient() {
		d = 2
	}
	waitID := d * uint64(time.Now().Unix())
	fmt.Printf("create a token with wait: %d\n", waitID)
	msg.wait = waitID
	if !c.write(msg) {
		return Message{}, false
	}

	ch := make(chan Message)
	c.waitingMessages[msg.wait] = ch
	msg, ok := <-ch
	delete(c.waitingMessages, msg.wait)
	fmt.Printf("got waiting message: %d\n", msg.wait)

	return msg, ok
}

// DisconnectFrom gracefully disconnects from a namespace.
func (c *conn) DisconnectFrom(namespace string) error {
	c.mu.RLock()
	ns, ok := c.connectedNamespaces[namespace]
	c.mu.RUnlock()

	if !ok {
		return ErrBadNamespace
	}

	disconnectMsg := Message{Namespace: namespace, Event: OnNamespaceDisconnect, isDisconnect: true}
	err := c.writeDisconnect(disconnectMsg, true)
	if err != nil {
		return err
	}

	return ns.events.fireEvent(ns, disconnectMsg)
}

func (c *conn) writeDisconnect(disconnectMsg Message, lock bool) error {
	if c.IsClient() {
		// println("client: before ask")
		msg, ok := c.ask(disconnectMsg)
		// println("client: after ask")
		if !ok {
			return ErrWrite
		}

		if !msg.isError {
			// if all ok, remove it.
			// println("client: remove-disconnect namespace: " + disconnectMsg.Namespace)
			c.deleteNSConn(disconnectMsg.Namespace, lock)
		}

		return msg.Err
	}
	// when server calls it.
	c.deleteNSConn(disconnectMsg.Namespace, lock)

	c.write(disconnectMsg) // we don't care about client to respond, server can force disconnect action of a connection from a namespace.
	return nil
} // TODO: "exit" does not work.

var ErrWrite = fmt.Errorf("write closed")

func (c *conn) Connect(namespace string) (NSConn, error) {
	c.mu.RLock()
	ns, alreadyConnected := c.connectedNamespaces[namespace]
	c.mu.RUnlock()
	if alreadyConnected {
		// println(namespace + " already connected")
		return ns, nil
	}

	events, ok := c.namespaces[namespace]
	if !ok {
		return nil, ErrBadNamespace
	}

	// println("sending connect...")
	msg, ok := c.ask(Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		isConnect: true,
	})

	// println("connect response received...")
	if !ok {
		return nil, ErrWrite
	}

	if msg.isError {
		if msg.Err.Error() == ErrBadNamespace.Error() {
			return nil, ErrBadNamespace
		}
		return nil, msg.Err
	}

	c.mu.RLock()
	ns, alreadyConnected = c.connectedNamespaces[namespace]
	c.mu.RUnlock()
	if alreadyConnected {
		// println(namespace + " already connected [2]")
		return ns, nil
	}

	ns = newNSConn(c, namespace, events)

	connectMessage := Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		isConnect: true,
	}

	err := events.fireEvent(ns, connectMessage)
	if err != nil {
		return nil, err
	}

	c.addNSConn(ns)

	connectMessage.Event = OnNamespaceConnected
	events.fireEvent(ns, connectMessage)
	// if !c.IsClient() && !msg.isNoOp {
	// 	c.write(connectMessage)
	// }

	return ns, err
}

func (c *conn) getNSConn(namespace string) (NSConn, error) {
	c.mu.RLock()
	ns, ok := c.connectedNamespaces[namespace]
	c.mu.RUnlock()
	if ok {
		return ns, nil
	}

	events, ok := c.namespaces[namespace]
	if !ok {
		return nil, ErrBadNamespace
	}

	if c.IsClient() {
		msg, ok := c.ask(Message{
			Namespace: namespace,
			Event:     OnNamespaceConnect,
			isConnect: true,
		})
		if !ok {
			return nil, ErrWrite
		}

		if msg.isError {
			if msg.Err.Error() == ErrBadNamespace.Error() {
				return nil, ErrBadNamespace
			}
		}

		ns = newNSConn(c, namespace, events)
		c.addNSConn(ns)
	}

	return ns, nil
}

// DisconnectFromAll gracefully disconnects from all namespaces.
func (c *conn) DisconnectFromAll() {
	c.mu.Lock()
	disconnectMsg := Message{Event: OnNamespaceDisconnect, isDisconnect: true}
	for namespace := range c.connectedNamespaces {
		disconnectMsg.Namespace = namespace
		c.writeDisconnect(disconnectMsg, false)
	}
	c.mu.Unlock()
}

func (c *conn) isClosed() bool {
	return atomic.LoadUint32(c.once) > 0
}

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(c.once, 0, 1) {
		// fire the namespaces' disconnect event for both server and client.
		disconnectMsg := Message{Event: OnNamespaceDisconnect, isDisconnect: true}
		for _, ns := range c.connectedNamespaces {
			disconnectMsg.Namespace = ns.namespace
			ns.events.fireOnNamespaceDisconnect(ns, disconnectMsg)
		}

		go func() {
			if c.server != nil {
				c.server.disconnect <- c
			} else {
				// close(c.out)
			}

			close(c.closeCh)
		}()

		c.underline.NetConn.Close()
	}
}

func (c *conn) Write(namespace, event string, body []byte) bool {
	// log.Printf("conn:Write:\nnamespace:%s\nevent:%s\nbody:%s", namespace, event, string(body))
	return c.write(Message{
		Namespace: namespace,
		Event:     event,
		Body:      body,
	})
}

func (c *conn) WriteAndWait(namespace, event string, body []byte) Message {
	response, ok := c.ask(Message{
		Namespace: namespace,
		Event:     event,
		Body:      body,
	})

	if !ok {
		return Message{Err: ErrWrite, isError: true}
	}

	return response
}

// func (c *conn) WriteWithCallback(namespace, event string, body []byte, callback func(msg Message) error) error {
// 	response := c.WriteAndWait(namespace, event, body)
// 	if callback == nil {
// 		// callback can be nil when caller does not care about the reply
// 		// and wants to know if the "remote command" was successfully ran.
// 		return response.Err
// 	}

// 	return callback(response)
// }

func (c *conn) write(msg Message) bool {
	msg.from = c.ID()

	if !msg.isConnect && !msg.isDisconnect {
		c.mu.RLock()
		_, ok := c.connectedNamespaces[msg.Namespace]
		c.mu.RUnlock()
		if !ok {
			return false
		}
	}

	select {
	case <-c.closeCh:
		return false
	case c.out <- serializeMessage(nil, msg):
		return true
	}
}
