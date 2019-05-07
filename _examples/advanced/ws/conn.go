package ws

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Socket interface {
	NetConn() net.Conn
	Request() *http.Request
	ReadText(timeout time.Duration) (body []byte, err error)
	WriteText(body []byte, timeout time.Duration) error
}

// type Socket interface {
// 	UnderlyingSocket
// 	Request() *http.Request
// }

// type socket struct {
// 	UnderlyingSocket
// 	Request *http.Request
// }

// func wrapSocket(s UnderlyingSocket, r *http.Request) *socket {
// 	return &socket{
// 		UnderlyingSocket: s,
// 		Request:          r,
// 	}
// }

type Conn interface {
	Socket() Socket
	ID() string
	Write(namespace string, event string, body []byte) bool
	WriteAndWait(ctx context.Context, namespace, event string, body []byte) Message
	String() string
	Connect(ctx context.Context, namespace string) (NSConn, error)
	WaitConnect(ctx context.Context, namespace string) (NSConn, error)
	DisconnectFrom(ctx context.Context, namespace string) error
	DisconnectFromAll(ctx context.Context) error

	IsClient() bool
	Server() *Server

	Close()
	IsClosed() bool
}

type NSConn interface {
	Conn
	Emit(event string, body []byte) bool
	Ask(ctx context.Context, event string, body []byte) Message
	Room(ctx context.Context, roomName string) (*Room, error)
	Disconnect(ctx context.Context) error
}

// IsDisconnected reports whether the "err" is a timeout or a closed connection error.
func IsDisconnected(err error) bool {
	if err == nil {
		return false
	}

	return IsClosed(err) || IsTimeout(err)
}

// IsClosed reports whether the "err" is a "closed by the remote host" network connection error.
func IsClosed(err error) bool {
	if err == nil {
		return false
	}

	if _, ok := err.(CloseError); ok {
		return true
	}

	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return true
	}

	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Err == nil {
			return false
		}

		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			if sysErr.Err == nil {
				return false
			}
			// return strings.HasSuffix(sysErr.Err.Error(), "closed by the remote host.")
			return true
		}

		return strings.HasSuffix(err.Error(), "use of closed network connection")
	}

	return false
}

// IsTimeout reports whether the "err" is caused by a defined timeout,
// callers can add timeouts via `Dial` or via `Conn.WriteTimeout`, `Conn.ReadTimeout`, `FastWS.HandshakeTimeout`.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}

	if netErr, ok := err.(*net.OpError); ok {
		// poll.TimeoutError is the /internal/poll of the go language itself, we can't use it directly.
		return netErr.Timeout()
	}

	return false
}

type connectedRooms struct {
}

type conn struct {
	id string

	underline  Socket
	namespaces Namespaces

	connectedNamespaces *connectedNamespaces

	// connectedNamespacesWaiters map[string]chan struct{}

	closeCh chan struct{}
	// out     chan []byte
	// broadcastChannel chan Message // server-side only.
	// in   chan []byte
	once *uint32

	mu              sync.RWMutex
	waitingMessages map[string]chan Message // messages that this connection waits for a reply.

	server *Server

	acknowledged *uint32

	// ReadTimeout time allowed to read a message from the connection, can be altered before `Read` and `Decode`.
	// It is available for both server and client sides at `Server#OnConnected` and after `Dial`.
	// Defaults to no timeout.
	ReadTimeout time.Duration
	// WriteTimeout time allowed to write a message to the connection, can be altered before `Write`, `WriteWithCode` and `Encoder`.
	// Defaults to no timeout.
	WriteTimeout time.Duration
}

func newConn(underline Socket, namespaces Namespaces) *conn {
	c := &conn{
		underline:  underline,
		namespaces: namespaces,
		connectedNamespaces: &connectedNamespaces{
			namespaces: make(map[string]*nsConn),
		},
		// connectedNamespacesWaiters: make(map[string]chan struct{}),
		closeCh: make(chan struct{}),
		// out:             make(chan []byte, 256),
		// in:              make(chan []byte, 256),
		once:            new(uint32),
		acknowledged:    new(uint32),
		waitingMessages: make(map[string]chan Message),
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

var (
	ackBinary   = []byte("ack")
	ackOKBinary = []byte("ack_ok")
)

func (c *conn) isAcknowledged() bool {
	return atomic.LoadUint32(c.acknowledged) > 0
}

func (c *conn) startReader() {
	// c.underline.ReadText(0) // ignore first message, wait.

	// println("ignoring: " + string(b))
	if c.IsClosed() {
		return
	}

	defer c.Close()

	queue := make([]*Message, 0)
	handleQueue := func() {
		for _, msg := range queue {
			// fmt.Printf("queue: handle %s/%s\n", msg.Namespace, msg.Event)
			c.handleMessage(*msg)
		}

		queue = nil
	}

	for {
		b, err := c.underline.ReadText(c.ReadTimeout)
		if err != nil {
			return
		}

		if bytes.HasPrefix(b, ackBinary) {
			if c.IsClient() {
				id := string(b[len(ackBinary):])
				c.id = id
				atomic.StoreUint32(c.acknowledged, 1)
				c.underline.WriteText(ackOKBinary, c.WriteTimeout)
				handleQueue()
			} else {
				if len(b) == len(ackBinary) {
					c.underline.WriteText(append(ackBinary, []byte(c.id)...), c.WriteTimeout)
				} else {
					// its ackOK, answer from client when ID received and it's ready for write/read.
					atomic.StoreUint32(c.acknowledged, 1)
					handleQueue()
				}
			}

			continue
		}

		msg := deserializeMessage(nil, b)
		if msg.isInvalid {
			fmt.Printf("%s[%d] is invalid payload\n", b, len(b))
			// continue // or return (close)?
			continue
		}

		if !c.isAcknowledged() {
			// fmt.Printf("queue: add %s/%s\n%#+v\n", msg.Namespace, msg.Event, msg)
			queue = append(queue, &msg)
			continue
		}

		//	fmt.Printf("=============\n%s\n%#+v\n=============\n", string(b), msg)
		if !c.handleMessage(msg) {
			println(msg.Event + " not handled")
			continue
		}
	}
}

func (c *conn) handleMessage(msg Message) bool {
	if msg.wait != "" {
		c.mu.RLock()
		ch, ok := c.waitingMessages[msg.wait]
		c.mu.RUnlock()
		if ok {
			// fmt.Printf("msg wait: %d for event: %s | isDisconnect: %v\n", msg.wait, msg.Event, msg.isDisconnect)
			ch <- msg
			return true
		}
	}

	switch msg.Event {
	case OnNamespaceConnect:
		c.connectedNamespaces.replyConnect(c, msg)
	case OnNamespaceDisconnect:
		c.connectedNamespaces.replyDisconnect(c, msg)
	case OnRoomJoin:
		c.connectedNamespaces.get(msg.Namespace).replyRoomJoin(msg)
	case OnRoomLeave:
		c.connectedNamespaces.get(msg.Namespace).replyRoomLeave(msg)
	default:
		msg.IsLocal = false
		ns := c.connectedNamespaces.get(msg.Namespace)
		if ns != nil {
			err := ns.events.fireEvent(ns, msg)
			if err != nil {
				msg.Err = err
				c.write(msg)
				if isCloseError(err) {
					return false // close the connection.
				}
			}

		}
	}

	return true
}

func (c *conn) ID() string {
	return c.id
}

func (c *conn) String() string {
	return c.ID()
}

func (c *conn) Socket() Socket {
	return c.underline
}

func (c *conn) ask(ctx context.Context, msg Message) (Message, error) {
	if c.IsClosed() {
		println("here 1")
		return msg, CloseError{Code: -1, error: ErrWrite}
	}

	// // block until ACK-ed.
	// for !c.isAcknowledged() {
	// 	// println("no ACK but ask")
	// }

	// waitID := d * uint64(time.Now().Unix())
	// fmt.Printf("create a token with wait: %d for msg.Event: %s \n", waitID, msg.Event)
	// msg.wait = waitID

	// if c.IsClient() {
	// 	msg.wait = 1
	// } else {
	// 	msg.wait = 2
	// }

	// msg.wait = time.Now().String() //
	now := time.Now().UnixNano()
	// second := rand.New(rand.NewSource(now)).Int63n(50-25) + 25
	// msg.wait = strconv.FormatInt(now, 10) + "_" + strconv.FormatInt(second, 10)
	msg.wait = strconv.FormatInt(now, 10)
	if c.IsClient() {
		msg.wait = "client_" + msg.wait
	}

	if ctx == nil {
		ctx = context.TODO()
	} else {
		if deadline, has := ctx.Deadline(); has {
			if deadline.Before(time.Now().Add(-1 * time.Second)) {
				return Message{}, context.DeadlineExceeded
			}
		}
	}

	ch := make(chan Message)
	c.mu.Lock()
	c.waitingMessages[msg.wait] = ch
	c.mu.Unlock()

	if !c.write(msg) {
		println("here 2")
		return Message{}, ErrWrite
	}

	// receive := <-ch
	// c.mu.Lock()
	// delete(c.waitingMessages, receive.wait)
	// c.mu.Unlock()
	//fmt.Printf("got waiting message: %d\n", msg.wait)
	// return receive, nil

	select {
	case <-ctx.Done():
		// fmt.Printf("[%s] isClosed=%v | timeout for message: %#+v\n", c.ID(), c.IsClosed(), msg)
		c.Close()
		// return Message{}, ctx.Err()
		// if msg.isConnect || msg.isDisconnect || c.isClosed() {
		// 	close(ch)
		// 	c.Close()
		// 	return Message{}, true
		// }

		return Message{}, ctx.Err()
	case receive := <-ch:
		c.mu.Lock()
		delete(c.waitingMessages, receive.wait)
		c.mu.Unlock()

		return receive, receive.Err
	}
}

// DisconnectFrom gracefully disconnects from a namespace.
func (c *conn) DisconnectFrom(ctx context.Context, namespace string) error {
	return c.connectedNamespaces.askDisconnect(ctx, c, Message{
		Namespace: namespace,
		Event:     OnNamespaceDisconnect,
	}, true)
}

var ErrWrite = fmt.Errorf("write closed")

// const defaultWaitServerOrClientConnectTimeout = 8 * time.Second

// const waitConnectDuruation = 100 * time.Millisecond

// Nil context means try without timeout, wait until it connects to the specific namespace.
func (c *conn) WaitConnect(ctx context.Context, namespace string) (NSConn, error) {
	var (
		ns NSConn
	)
	// c.mu.RLock()
	// ns, found := c.connectedNamespaces[namespace]
	// c.mu.RUnlock()
	// if found {
	// 	return ns, nil
	// }

	if ctx == nil {
		ctx = context.TODO()
	}

	// if _, hasDeadline := ctx.Deadline(); !hasDeadline {
	// 	var cancel context.CancelFunc
	// 	ctx, cancel = context.WithTimeout(ctx, defaultWaitServerOrClientConnectTimeout)
	// 	defer cancel()
	// }

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			if ns == nil {
				ns = c.connectedNamespaces.get(namespace)
			}

			if ns != nil && c.isAcknowledged() {
				return ns, nil
			}

			time.Sleep(syncWaitDur)
		}
	}

	// waiter := make(chan struct{})
	// c.connectedNamespacesWaiters[namespace] = waiter
	// for {
	// 	select {
	// 	case <-ctx.Done():
	// 		return nil, ctx.Err()
	// 	case <-waiter:
	// 		ns, found = c.connectedNamespaces[namespace]
	// 		if found {
	// 			return ns, nil
	// 		}
	// 	}
	// }

	return nil, ErrBadNamespace
}

const syncWaitDur = 15 * time.Millisecond

func (c *conn) Connect(ctx context.Context, namespace string) (NSConn, error) {
	if !c.IsClient() {
		for !c.isAcknowledged() {
			time.Sleep(syncWaitDur)
		}
	}

	return c.connectedNamespaces.askConnect(ctx, c, namespace)
}

// DisconnectFromAll gracefully disconnects from all namespaces.
func (c *conn) DisconnectFromAll(ctx context.Context) error {
	return c.connectedNamespaces.disconnectAll(ctx, c)
}

func (c *conn) IsClosed() bool {
	return atomic.LoadUint32(c.once) > 0
}

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(c.once, 0, 1) {
		close(c.closeCh)
		// fire the namespaces' disconnect event for both server and client.
		c.connectedNamespaces.forceDisconnectAll()

		c.mu.Lock()
		for wait := range c.waitingMessages {
			delete(c.waitingMessages, wait)
		}
		c.mu.Unlock()

		atomic.StoreUint32(c.acknowledged, 0)

		go func() {
			if c.server != nil {
				c.server.disconnect <- c
			} else {
				// // close(c.out)
			}

			//	close(c.closeCh)
		}()

		c.underline.NetConn().Close()
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

func (c *conn) WriteAndWait(ctx context.Context, namespace, event string, body []byte) Message {
	response, err := c.ask(ctx, Message{
		Namespace: namespace,
		Event:     event,
		Body:      body,
	})

	if !response.isError && err != nil {
		return Message{Err: err, isError: true}
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
	if c.IsClosed() {
		return false
	}

	// msg.from = c.ID()

	if !msg.isConnect() && !msg.isDisconnect() {
		ns := c.connectedNamespaces.get(msg.Namespace)
		if ns == nil {
			// println("namespace " + msg.Namespace + " not found")
			return false
		}

		if msg.Room != "" && !msg.isRoomJoin() && !msg.isRoomLeft() {
			ns.roomsMu.RLock()
			_, ok := ns.rooms[msg.Room]
			ns.roomsMu.RUnlock()
			if !ok {
				// tried to send to a not joined room.
				return false
			}
		}
	}

	err := c.underline.WriteText(serializeMessage(nil, msg), c.WriteTimeout)
	// _, err := c.underline.NetConn.Write(serializeMessage(nil, msg))
	if err != nil {
		if IsClosed(err) {
			c.Close()
		}
		// c.underline.HandleError(err)
		// println("err on write: " + err.Error() + "\n" + msg.Event + ": " + string(msg.Body))
		return false
	}

	// No need: underline writer checks by itself if len(b) > available size buffer
	// and writes all if needed.
	// if n < len(b) {
	// 	rem := b[n:]
	//  [...]
	// }

	// for i, n := 0, len(c.out); i < n; i++ {
	// 	c.underline.Write(newline)
	// 	c.underline.Write(<-c.out)
	// }

	// err = c.underline.Writer.Flush()
	// return err == nil
	return true
}
