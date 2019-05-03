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

	"github.com/iris-contrib/go.uuid"
)

type Socket interface {
	NetConn() net.Conn
	ReadText(timeout time.Duration) (body []byte, err error)
	WriteText(body []byte, timeout time.Duration) error
}

// IDGenerator is the type of function that it is used
// to generate unique identifiers for new connections.
//
// See `FastWS.IDGenerator`.
type IDGenerator func(w http.ResponseWriter, r *http.Request) string

// DefaultIDGenerator returns a random unique for a new connection.
var DefaultIDGenerator IDGenerator = func(http.ResponseWriter, *http.Request) string {
	id, err := uuid.NewV4()
	if err != nil {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return id.String()
}

type Conn interface {
	NetConn() net.Conn
	Socket() Socket
	ID() string
	Write(namespace string, event string, body []byte) bool
	WriteAndWait(ctx context.Context, namespace, event string, body []byte) Message
	String() string
	Connect(ctx context.Context, namespace string) (NSConn, error)
	WaitConnect(ctx context.Context, namespace string) (NSConn, error)
	DisconnectFrom(ctx context.Context, namespace string) error
	DisconnectFromAll(ctx context.Context)

	IsClient() bool
	Server() *Server

	Close()
	IsClosed() bool

	Acknowledged() bool
}

type NSConn interface {
	Conn
	Emit(event string, body []byte) bool
	Ask(ctx context.Context, event string, body []byte) Message
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

type conn struct {
	id      string
	netConn net.Conn

	underline  Socket
	namespaces Namespaces

	connectedNamespaces map[string]*nsConn
	mu                  sync.RWMutex
	// connectedNamespacesWaiters map[string]chan struct{}

	closeCh chan struct{}
	// out     chan []byte
	// broadcastChannel chan Message // server-side only.
	// in   chan []byte
	once *uint32

	waitingMessages map[string]chan Message // messages that this connection waits for a reply.
	writeMu         sync.Mutex

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
		underline:           underline,
		namespaces:          namespaces,
		connectedNamespaces: make(map[string]*nsConn),
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

func (c *conn) NetConn() net.Conn {
	return c.netConn
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

func (c *conn) Acknowledged() bool {
	return atomic.LoadUint32(c.acknowledged) > 0
}

var (
	newline = []byte{'\n'}
)

func (c *conn) startWriter() {
	// if c.IsClosed() {
	// 	return
	// }

	// defer c.Close()

	// for b := range c.out {
	// 	_, err := c.underline.Write(b)
	// 	if err != nil {
	// 		c.underline.HandleError(err)
	// 		return
	// 	}

	// 	// No need: underline writer checks by itself if len(b) > available size buffer
	// 	// and writes all if needed.
	// 	// if n < len(b) {
	// 	// 	rem := b[n:]
	// 	//  [...]
	// 	// }

	// 	// for i, n := 0, len(c.out); i < n; i++ {
	// 	// 	c.underline.Write(newline)
	// 	// 	c.underline.Write(<-c.out)
	// 	// }

	// 	c.underline.Writer.Flush()
	// }
	// for {
	// 	select {
	// 	case <-c.closeCh:
	// 		return
	// 	case b, ok := <-c.out:
	// 		if !ok {
	// 			return
	// 		}
	// 		_, err := c.underline.Write(b)
	// 		if err != nil {
	// 			c.underline.HandleError(err)
	// 			return
	// 		}

	// 		// No need: underline writer checks by itself if len(b) > available size buffer
	// 		// and writes all if needed.
	// 		// if n < len(b) {
	// 		// 	rem := b[n:]
	// 		//  [...]
	// 		// }

	// 		// for i, n := 0, len(c.out); i < n; i++ {
	// 		// 	c.underline.Write(newline)
	// 		// 	c.underline.Write(<-c.out)
	// 		// }

	// 		c.underline.Writer.Flush()
	// 	}
	// }
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

	// if c.IsClient() {
	// 	// c.out <- ackBinary
	// 	c.underline.Write(ackBinary)
	// }

	// if c.IsClient() {
	// 	c.underline.ReadText(c.ReadTimeout)
	// }

	// readLoop:
	for {
		// if c.ReadTimeout > 0 {
		// 	c.underline.NetConn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
		// }

		// header, err := ws.ReadHeader(c.underline.NetConn)
		// if err != nil {
		// 	return
		// }

		// if header.Length > 300 {
		// 	println("TOO BIG HEADER for our app")
		// 	if header.Length >= ws.MaxHeaderSize {
		// 		println("ALSO TOO BIG for the websocket entirely")
		// 	}

		// 	print("masked=")
		// 	println(header.Masked)
		// 	print("length=")
		// 	println(header.Length)
		// 	var b []byte
		// 	if header.Length > 10000 {
		// 		b = make([]byte, 300)
		// 	} else {
		// 		b = make([]byte, header.Length)
		// 	}
		// 	print("start reading the first ")
		// 	println(cap(b))

		// 	_, err = io.ReadFull(c.underline.NetConn, b)
		// 	if header.Masked {
		// 		ws.Cipher(b, header.Mask, 0)
		// 	}

		// 	fmt.Printf("after mask: %s\n", string(b))

		// 	return
		// }

		// // 	println(header.Length)

		// b := make([]byte, header.Length)
		// n, err := io.ReadFull(c.underline.NetConn, b)
		// if err != nil && int64(n) != header.Length {
		// 	fmt.Printf("[%d/%d] read from conn: %v\n", n, header.Length, err)
		// 	return
		// }

		// if header.Masked {
		// 	ws.Cipher(b, header.Mask, 0)
		// }

		// fmt.Printf("Header size[%d]\nData: %s\n", header.Length, string(b))

		// b := make([]byte, 300)

		// _, err := io.ReadFull(c.underline, b)
		// if err != nil {
		// 	if err != io.EOF && err != io.ErrUnexpectedEOF {
		// 		println(err.Error())
		// 		return
		// 	}
		// 	if !c.IsClient() {
		// 		ws.Cipher(b, nil, 0)
		// 	}

		// 	ws.Header

		// }

		b, err := c.underline.ReadText(c.ReadTimeout)
		if err != nil {
			return
		}

		// go func(b []byte) {
		// fmt.Printf("%s\n\n", string(b))

		if bytes.HasPrefix(b, ackBinary) {
			if c.IsClient() {
				id := string(b[len(ackBinary):])
				c.id = id
				atomic.StoreUint32(c.acknowledged, 1)
				// c.out <- ackOKBinary
				c.underline.WriteText(ackOKBinary, c.WriteTimeout)
				handleQueue()

				// println("got ID " + id)
			} else {
				if len(b) == len(ackBinary) {
					// c.out <- append(ackBinary, []byte(c.id)...)
					//	c.underline.WriteText(trashMessage, c.WriteTimeout)
					c.underline.WriteText(append(ackBinary, []byte(c.id)...), c.WriteTimeout)
					// println("sent ID: " + c.id)
				} else {
					// its ackOK, answer from client when ID received and it's ready for write/read.
					atomic.StoreUint32(c.acknowledged, 1)
					// println("ACK-ed")
					handleQueue()
				}
			}

			// continue readLoop
			continue
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
		if msg.isInvalid {
			fmt.Printf("%s[%d] is invalid payload\n", b, len(b))
			// continue // or return (close)?
			continue
		}

		if !c.isAcknowledged() {
			// fmt.Printf("queue: add %s/%s\n%#+v\n", msg.Namespace, msg.Event, msg)
			queue = append(queue, &msg)
			// continue
			continue
		}

		//	fmt.Printf("=============\n%s\n%#+v\n=============\n", string(b), msg)
		if !c.handleMessage(msg) {
			println(msg.Event + " not handled")
			continue
		}

		// if msg.isConnect {
		// 	if ch, ok := c.connectedNamespacesWaiters[msg.Namespace]; ok {
		// 		close(ch)
		// 		delete(c.connectedNamespacesWaiters, msg.Namespace)
		// 	}
		// }

		// if msg.wait > 0 {
		// 	if ch, ok := c.waitingMessages[msg.wait]; ok {
		// 		// fmt.Printf("msg wait: %d for event: %s | isDisconnect: %v\n", msg.wait, msg.Event, msg.isDisconnect)
		// 		ch <- msg
		// 		continue readLoop
		// 	}
		// }

		// for _, h := range messageHandlers {
		// 	handled, err := h(c, msg)
		// 	if err != nil {
		// 		msg.Body = nil
		// 		msg.Err = err
		// 		c.write(msg)
		// 		if isCloseError(err) {
		// 			return // close the connection.
		// 		}
		// 		continue readLoop
		// 	}

		// 	if handled {
		// 		break
		// 	}
		// }
		// }(b)

	}

}

func (c *conn) handleMessage(msg Message) bool {
	if msg.isConnect == false && msg.isDisconnect == false && msg.isNoOp {
		c.mu.RLock()
		println("sending msg noop to waiting messages")
		// is connect on bad form.
		for _, ch := range c.waitingMessages {
			ch <- msg
		}
		c.mu.RUnlock()
	}

	if msg.wait != "" {
		c.mu.RLock()
		ch, ok := c.waitingMessages[msg.wait]
		c.mu.RUnlock()
		if ok {
			// fmt.Printf("msg wait: %d for event: %s | isDisconnect: %v\n", msg.wait, msg.Event, msg.isDisconnect)
			ch <- msg
			return true
		} else {
			// println(msg.wait + " wait Event: " + msg.Event + " not found")
		}
	}

	for _, h := range messageHandlers {
		handled, err := h(c, msg)
		if err != nil {
			msg.Body = nil
			msg.Err = err
			c.write(msg)
			if isCloseError(err) {
				return false // close the connection.
			}
			return true
		}

		if handled {
			return true
		}
	}

	return false
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
		// re-ask.

		if receive.isNoOp && receive.isConnect == false && receive.isDisconnect == false {
			println("re-asking...")
			return c.ask(nil, msg)
		}
		return receive, nil
	}
}

// DisconnectFrom gracefully disconnects from a namespace.
func (c *conn) DisconnectFrom(ctx context.Context, namespace string) error {
	c.mu.RLock()
	ns, ok := c.connectedNamespaces[namespace]
	c.mu.RUnlock()

	if !ok {
		return ErrBadNamespace
	}

	disconnectMsg := Message{Namespace: namespace, Event: OnNamespaceDisconnect, isDisconnect: true}
	err := c.writeDisconnect(ctx, disconnectMsg, true)
	if err != nil {
		return err
	}

	return ns.events.fireEvent(ns, disconnectMsg)
}

func (c *conn) writeDisconnect(ctx context.Context, disconnectMsg Message, lock bool) error {
	if c.IsClient() {
		// println("client: before ask")
		msg, err := c.ask(ctx, disconnectMsg)
		// println("client: after ask")
		if err != nil {
			return err
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

// const defaultWaitServerOrClientConnectTimeout = 8 * time.Second

// const waitConnectDuruation = 100 * time.Millisecond

// Nil context means try without timeout, wait until it connects to the specific namespace.
func (c *conn) WaitConnect(ctx context.Context, namespace string) (NSConn, error) {
	var (
		ns    NSConn
		found bool
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
			if !found {
				c.mu.RLock()
				ns, found = c.connectedNamespaces[namespace]
				c.mu.RUnlock()
			}

			if found && c.isAcknowledged() {
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

	// for !c.isAcknowledged() {
	// 	time.Sleep(syncWaitDur)
	// }

	// c.mu.RLock()
	ns, alreadyConnected := c.connectedNamespaces[namespace]
	// c.mu.RUnlock()
	if alreadyConnected {
		return ns, nil
	}

	events, ok := c.namespaces[namespace]
	if !ok {
		return nil, ErrBadNamespace
	}

	msg, err := c.ask(ctx, Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		isConnect: true,
	})

	if err != nil {
		return nil, err
	}

	if msg.isError {
		if msg.Err.Error() == ErrBadNamespace.Error() {
			return nil, ErrBadNamespace
		}
		return nil, msg.Err
	}

	// re-check, maybe local connected.
	// c.mu.RLock()
	ns, alreadyConnected = c.connectedNamespaces[namespace]
	// c.mu.RUnlock()
	if alreadyConnected {
		return ns, nil
	}

	ns = newNSConn(c, namespace, events)

	connectMessage := Message{
		Namespace: namespace,
		Event:     OnNamespaceConnect,
		isConnect: true,
	}

	err = events.fireEvent(ns, connectMessage)
	if err != nil {
		return nil, err
	}

	c.addNSConn(ns)

	connectMessage.Event = OnNamespaceConnected
	connectMessage.isConnect = false
	events.fireEvent(ns, connectMessage)

	return ns, err
}

// DisconnectFromAll gracefully disconnects from all namespaces.
func (c *conn) DisconnectFromAll(ctx context.Context) {
	c.mu.Lock()
	disconnectMsg := Message{Event: OnNamespaceDisconnect, isDisconnect: true}
	for namespace := range c.connectedNamespaces {
		disconnectMsg.Namespace = namespace
		c.writeDisconnect(ctx, disconnectMsg, false)
	}
	c.mu.Unlock()
}

func (c *conn) IsClosed() bool {
	return atomic.LoadUint32(c.once) > 0
}

func (c *conn) Close() {
	if atomic.CompareAndSwapUint32(c.once, 0, 1) {
		close(c.closeCh)
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

	if err != nil {
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

	if !msg.isConnect && !msg.isDisconnect {
		c.mu.RLock()
		_, ok := c.connectedNamespaces[msg.Namespace]
		c.mu.RUnlock()
		if !ok {
			// println("namespace " + msg.Namespace + " not found")
			return false
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
