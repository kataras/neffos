package ws

import (
	"bytes"
	"context"
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
	WaitConnect(ctx context.Context, namespace string) (NSConn, error)
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
	// broadcastChannel chan Message // server-side only.
	in   chan []byte
	once *uint32

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
		in:                  make(chan []byte, 1),
		once:                new(uint32),
		waitingMessages:     make(map[uint64]chan Message),
		//	broadcastChannel:    make(chan Message), // not used in client-side.
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

var (
	newline = []byte{'\n'}
)

func (c *conn) startWriter() {
	if c.isClosed() {
		return
	}

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
			// case msg, ok := <-c.broadcastChannel:
			// 	if !ok {
			// 		return
			// 	}

			// 	if msg.from == c.underline.ID {
			// 		continue
			// 	}
			// 	c.write(msg)
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

var ackBinary = []byte("ACK")

func (c *conn) startReader() {
	if c.isClosed() {
		return
	}

	defer c.Close()

	go func() {
		defer c.Close()

		for {
			select {
			case <-c.closeCh:
				return
			default:
				b, err := c.underline.ReadBinary()
				if err != nil {
					return
				}

				c.in <- b
			}
		}
	}()

	if c.IsClient() {
		c.out <- ackBinary
	}

readLoop:
	for {
		if c.isClosed() {
			return
		}

		select {
		case <-c.closeCh:
			// println("closed channel received.")
			return
		case b, ok := <-c.in:
			if !ok {
				return
			}

			if bytes.HasPrefix(b, ackBinary) {
				if c.IsClient() {
					id := string(b[len(ackBinary):])
					c.underline.ID = id
					// println("got ID " + id)
				} else {
					c.out <- append(ackBinary, []byte(c.underline.ID)...)
					// println("sent ID: " + c.underline.ID)
				}

				continue readLoop
			} else {
				// TODO FIX SOMETIMES CONNECT MESSAGE IS BEFORE ID.
				// if c.IsClient() {
				// 	if c.underline.ID == "" {
				// 		println("empty ID")
				// 		b, err := c.underline.ReadBinary()
				// 		if err != nil {
				// 			return
				// 		}
				// 		c.in <- b
				// 		continue readLoop
				// 	}
				// }
			}

			msg := deserializeMessage(nil, b)
			//	fmt.Printf("=============\n%s\n%#+v\n=============\n", string(b), msg)

			if msg.wait > 0 {
				if ch, ok := c.waitingMessages[msg.wait]; ok {
					// fmt.Printf("msg wait: %d for event: %s | isDisconnect: %v\n", msg.wait, msg.Event, msg.isDisconnect)
					ch <- msg
					continue readLoop
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

var askTimeout = 5 * time.Second

func (c *conn) ask(msg Message) (Message, bool) {
	if c.isClosed() {
		return msg, false
	}

	var d uint64 = 1
	if c.IsClient() {
		d = 2
	}
	waitID := d * uint64(time.Now().Unix())
	// fmt.Printf("create a token with wait: %d for msg.Event: %s \n", waitID, msg.Event)
	msg.wait = waitID
	if !c.write(msg) {
		return Message{}, false
	}

	ch := make(chan Message)
	c.mu.Lock()
	c.waitingMessages[msg.wait] = ch
	c.mu.Unlock()
	timer := time.NewTimer(askTimeout)
	defer timer.Stop()

	select {
	case <-timer.C:
		//	fmt.Printf("[%s] isClosed=%v | timeout for message: %#+v\n", c.ID(), c.isClosed(), msg)
		c.Close()
		return Message{}, false
		// if msg.isConnect || msg.isDisconnect || c.isClosed() {
		// 	close(ch)
		// 	c.Close()
		// 	return Message{}, true
		// }

		// return Message{}, false
	case msg, ok := <-ch:
		c.mu.Lock()
		delete(c.waitingMessages, msg.wait)
		c.mu.Unlock()
		return msg, ok
	}

	// msg, ok := <-ch
	// c.mu.Lock()
	// delete(c.waitingMessages, msg.wait)
	// c.mu.Unlock()
	//fmt.Printf("got waiting message: %d\n", msg.wait)

	// return msg, ok
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

const defaultWaitServerOrClientConnectTimeout = 3 * time.Second

func (c *conn) WaitConnect(ctx context.Context, namespace string) (NSConn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultWaitServerOrClientConnectTimeout)
		defer cancel()
	}

	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()

	var (
		ns    NSConn
		found bool
	)

	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, context.DeadlineExceeded
		default:
			//	println("searching")
			if !found {
				c.mu.RLock()
				ns, found = c.connectedNamespaces[namespace]
				c.mu.RUnlock()
			} else {
				if c.underline.ID != "" {
					return ns, nil
				}
			}
		}
	}
}

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
		return nil, msg.Err
	}

	// re-check, maybe local connected.
	c.mu.RLock()
	ns, alreadyConnected = c.connectedNamespaces[namespace]
	c.mu.RUnlock()
	if alreadyConnected {
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

		c.mu.Lock()
		for wait := range c.waitingMessages {
			delete(c.waitingMessages, wait)
		}
		c.mu.Unlock()

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

	// if !msg.isConnect && !msg.isDisconnect {
	// 	c.mu.RLock()
	// 	_, ok := c.connectedNamespaces[msg.Namespace]
	// 	c.mu.RUnlock()
	// 	if !ok {
	// 		return false
	// 	}
	// }

	select {
	case <-c.closeCh:
		return false
	case c.out <- serializeMessage(nil, msg):
		return true
	}
}
