package ws

import (
	"bytes"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/kataras/fastws"
)

type Conn interface {
	UnderlyingConn() *fastws.Conn
	ID() string
	Write(namespace string, event string, body []byte) bool
	WriteWithCallback(namespace, event string, body []byte, callback func(msg Message) error) error
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
	EmitWithCallback(event string, body []byte, callback func(msg Message) error) error
	Disconnect() error
}

type conn struct {
	underline  *fastws.Conn
	namespaces Namespaces

	connectedNamespaces map[string]*nsConn
	mu                  sync.RWMutex

	closeCh chan struct{}
	out     chan []byte
	once    uint32

	waitingMessages map[uint64]chan Message

	server *Server
}

func newConn(underline *fastws.Conn, namespaces Namespaces) *conn {
	c := &conn{
		underline:           underline,
		namespaces:          namespaces,
		connectedNamespaces: make(map[string]*nsConn),
		closeCh:             make(chan struct{}),
		out:                 make(chan []byte, 256),
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
				ch <- msg
				continue
			}
		}

		if msg.isError {
			if msg.Event == "" { // global error, possible server-side.
				c.underline.HandleError(msg.Err)
				continue
			}

			c.mu.RLock()
			ns, ok := c.connectedNamespaces[msg.Namespace]
			c.mu.RUnlock()
			if !ok {
				continue
			}

			err := ns.events.fireEvent(ns, msg)
			if err != nil {
				msg.Err = err
				c.write(msg)
				if isCloseError(err) {
					return // close the connection.
				}
				continue
			}

			continue
		}

		if msg.isConnect {
			// came from server to the client.

			c.mu.RLock()
			_, alreadyConnected := c.connectedNamespaces[msg.Namespace]
			c.mu.RUnlock()

			if alreadyConnected {
				c.write(msg) // send the message back and continue, the client or server expects an answer.
				continue
			}

			events, ok := c.namespaces[msg.Namespace]
			if !ok {
				continue
			}

			ns := newNSConn(c, msg.Namespace, events)
			err = events.fireEvent(ns, msg)
			if err != nil {
				msg.Err = err
				msg.Body = nil
				c.write(msg)
				if isCloseError(err) {
					return // close the connection.
				}
				continue
			}

			c.addNSConn(ns)
			c.write(msg)
			continue
		}

		c.mu.RLock()
		nsConn, ok := c.connectedNamespaces[msg.Namespace]
		c.mu.RUnlock()
		if !ok {
			// see client.go#Connect for the client-side.
			continue
		}

		err = nsConn.events.fireEvent(nsConn, msg)
		if err != nil {
			msg.Err = err
			msg.Body = nil
			c.write(msg)
			if isCloseError(err) {
				return // close the connection.
			}
		} else if msg.isDisconnect {
			if !c.IsClient() {
				// send back the disconnect message without error.
				c.write(msg)
			}
			c.deleteNSConn(msg.Namespace, true)
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
	msg.wait = incrementCounter()
	// msg.wait = uint64(time.Now().Unix())
	if !c.write(msg) {
		return Message{}, false
	}

	ch := make(chan Message)
	c.waitingMessages[msg.wait] = ch
	msg, ok := <-ch
	delete(c.waitingMessages, msg.wait)
	// msg.wait = decrementCounter()
	decrementCounter()
	return msg, ok
}

// DisconnectFrom gracefully disconnects from a namespace.
func (c *conn) DisconnectFrom(namespace string) error {
	c.mu.RLock()
	ns := c.connectedNamespaces[namespace]
	c.mu.RUnlock()

	if ns == nil {
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
		msg, ok := c.ask(disconnectMsg)
		if !ok {
			return ErrWrite
		}

		if !msg.isError {
			// if all ok, remove it.
			c.deleteNSConn(disconnectMsg.Namespace, lock)
		}

		return msg.Err
	}
	// when server calls it.
	c.deleteNSConn(disconnectMsg.Namespace, lock)

	c.write(disconnectMsg) // we don't care about client to respond, server can force disconnect action of a connection from a namespace.
	return nil
}

var ErrWrite = fmt.Errorf("write closed")

func (c *conn) Connect(namespace string) (NSConn, error) {
	c.mu.RLock()
	ns, alreadyConnected := c.connectedNamespaces[namespace]
	c.mu.RUnlock()
	if alreadyConnected {
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
			return nil, msg.Err
		}
	}

	ns = newNSConn(c, namespace, events)
	c.addNSConn(ns)

	err := events.fireEvent(ns, Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		isConnect: true,
	})
	return ns, err
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
	return atomic.LoadUint32(&c.once) > 0
}

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(&c.once, 0, 1) {
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

func (c *conn) WriteWithCallback(namespace, event string, body []byte, callback func(msg Message) error) error {
	if callback == nil {
		ok := c.Write(namespace, event, body)
		if !ok {
			return ErrWrite
		}
	}

	response, ok := c.ask(Message{
		Namespace: namespace,
		Event:     event,
		Body:      body,
	})

	if !ok {
		return ErrWrite
	}

	return callback(response)
}

func (c *conn) write(msg Message) bool {
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
