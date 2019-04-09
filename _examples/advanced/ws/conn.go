package ws

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/kataras/fastws"
)

type Conn interface {
	UnderlyingConn() *fastws.Conn
	ID() string
	Write(namespace string, event string, body []byte) bool
	Close()
	String() string
	Connect(namespace string) (NSConn, error)
	IsClient() bool
	DisconnectFrom(namespace string) error
	DisconnectFromAll()
}

type NSConn interface {
	Conn
	Emit(event string, body []byte) bool
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

	waitingMessage chan Message
	waiting        uint32

	server *Server
}

func newConn(underline *fastws.Conn, namespaces Namespaces) *conn {
	c := &conn{
		underline:           underline,
		namespaces:          namespaces,
		connectedNamespaces: make(map[string]*nsConn),
		closeCh:             make(chan struct{}),
		out:                 make(chan []byte, 256),
		waitingMessage:      make(chan Message),
	}

	return c
}

func (c *conn) IsClient() bool {
	return c.server == nil
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
			println("DEBUG - ERROR conn.go: " + err.Error())
			return
		}
		msg := deserializeMessage(nil, b)

		if atomic.CompareAndSwapUint32(&c.waiting, 1, 0) {
			c.waitingMessage <- msg
			continue
		}

		// log.Printf("conn:startReader:msg:\n%#+v\n", msg)

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

		if msg.isConnect && !c.IsClient() {
			c.mu.RLock()
			_, alreadyConnected := c.connectedNamespaces[msg.Namespace]
			c.mu.RUnlock()

			if alreadyConnected {
				continue
			}

			events, ok := c.namespaces[msg.Namespace]
			if !ok {
				continue
			}

			conn := newNSConn(c, msg.Namespace, events)
			err = events.fireEvent(conn, msg)
			if err != nil {
				msg.Err = err
				msg.Body = nil
				c.write(msg)
				if isCloseError(err) {
					return // close the connection.
				}
			}

			c.addNSConn(conn)
			// send the ID with the same event.
			msg.Body = []byte(c.ID())
			c.write(msg)
			continue
		}

		c.mu.RLock()
		nsConn, ok := c.connectedNamespaces[msg.Namespace]
		c.mu.RUnlock()
		if !ok {
			println(msg.Namespace + " does not exist")
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
		} else if msg.isDisconnect && !c.IsClient() {
			c.deleteNSConn(msg.Namespace, true)
			// send back the disconnect message without error.
			c.write(msg)
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
	if !atomic.CompareAndSwapUint32(&c.waiting, 0, 1) {
		return Message{}, false // only one message can be waited, i.e user can't execute thins like Disconnect in a different go routine.
	}

	if !c.write(msg) {
		return Message{}, false
	}

	msg, ok := <-c.waitingMessage
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
	return c.writeDisconnect(disconnectMsg, true)
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

		c.underline.ID = string(msg.Body)
	}

	ns := newNSConn(c, namespace, events)
	c.addNSConn(ns)
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

func (c *conn) write(msg Message) bool {
	select {
	case <-c.closeCh:
		return false
	case c.out <- serializeMessage(nil, msg):
		return true
	}
}
