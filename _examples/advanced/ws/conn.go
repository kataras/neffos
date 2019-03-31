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
	Write(namespace string, event string, body []byte, err error)
	Close()
	String() string
	IsClient() bool
}

type NSConn interface {
	Conn
	Emit(event string, body []byte)
	Disconnect()
}

type conn struct {
	underline  *fastws.Conn
	namespaces Namespaces

	connectedNamespaces map[string]*nsConn
	mu                  sync.RWMutex

	closeCh chan struct{}
	out     chan []byte
	once    uint32

	server *Server
}

func newConn(underline *fastws.Conn, namespaces Namespaces) *conn {
	c := &conn{
		underline:           underline,
		namespaces:          namespaces,
		connectedNamespaces: make(map[string]*nsConn),
		closeCh:             make(chan struct{}),
		out:                 make(chan []byte, 256),
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
			return
		}

		msg := deserializeMessage(nil, b)

		// log.Printf("conn:startReader:msg:\n%#+v\n", msg)

		if msg.isConnect {
			if c.IsClient() {
				// client-side.

				// catch the ID of the namespace connect event.
				c.underline.ID = string(msg.Body)
				// log.Printf("SET ID: [%s]", c.ID())
			} else {
				// server-side.
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

				err := events.fireOnNamespaceConnect(conn, msg)
				if err != nil {
					msg.Body = nil
					msg.Err = err
					c.write(msg)
					if isCloseError(err) {
						return // close the connection.
					}
					continue
				}

				c.addNSConn(msg.Namespace, conn)
				// send the ID with the same event.
				msg.Body = []byte(c.ID())
				c.write(msg)
			}

			continue
		}

		if msg.isDisconnect {
			if !c.IsClient() {
				c.mu.RLock()
				conn, ok := c.connectedNamespaces[msg.Namespace]
				c.mu.RUnlock()
				if !ok {
					continue
				}

				events, ok := c.namespaces[msg.Namespace]
				if !ok {
					continue
				}

				err := events.fireOnNamespaceDisconnect(conn, msg)
				if err != nil {
					msg.Body = nil
					msg.Err = err
					c.write(msg)
					if isCloseError(err) {
						return // close the connection.
					}
					continue
				}

				c.mu.Lock()
				delete(c.connectedNamespaces, msg.Namespace)
				c.mu.Unlock()
			}

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
			c.Write(msg.Namespace, msg.Event, nil, err)
			if isCloseError(err) {
				return // close the connection.
			}
			continue
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

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(&c.once, 0, 1) {
		c.disconnectAll()
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

func (c *conn) addNSConn(namespace string, conn *nsConn) {
	c.mu.Lock()
	c.connectedNamespaces[namespace] = conn
	c.mu.Unlock()
}

func (c *conn) disconnect(namespace string) {
	c.mu.Lock()
	delete(c.connectedNamespaces, namespace)
	c.mu.Unlock()

	println("send disconnect message for namespace: " + namespace)
	c.write(Message{Namespace: namespace, isDisconnect: true})
}

func (c *conn) disconnectAll() {
	select {
	case <-c.closeCh:
		return
	default:
		println("conn.go:L260 send disconnect")
		c.mu.Lock()
		msg := Message{isDisconnect: true}
		for namespace := range c.connectedNamespaces {
			delete(c.connectedNamespaces, namespace)
			msg.Namespace = namespace
			c.write(msg)
		}
		c.mu.Unlock()
	}
}

func (c *conn) Write(namespace, event string, body []byte, err error) {
	// log.Printf("conn:Write:\nnamespace:%s\nevent:%s\nbody:%s\nerr:%v", namespace, event, string(body), err)
	c.write(Message{
		Namespace: namespace,
		Event:     event,
		Body:      body,
		Err:       err,
	})
}

func (c *conn) write(msg Message) {
	select {
	case <-c.closeCh:
		return
	case c.out <- serializeMessage(nil, msg):
	}
}
