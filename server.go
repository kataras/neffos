package neffos

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iris-contrib/go.uuid"
)

// Upgrader is the definition type of a protocol upgrader, gorilla or gobwas or custom.
// It is the first parameter of the `New` function which constructs a neffos server.
type Upgrader func(w http.ResponseWriter, r *http.Request) (Socket, error)

// IDGenerator is the type of function that it is used
// to generate unique identifiers for new connections.
//
// See `Server.IDGenerator`.
type IDGenerator func(w http.ResponseWriter, r *http.Request) string

// DefaultIDGenerator returns a random unique for a new connection.
var DefaultIDGenerator IDGenerator = func(http.ResponseWriter, *http.Request) string {
	id, err := uuid.NewV4()
	if err != nil {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return id.String()
}

// Server is the neffos server.
// Keeps the `IDGenerator` which can be customized, by default it's the `DefaultIDGenerator`  which
// generates connections unique identifiers using the uuid/v4.
//
// Callers can optionally register callbacks for connection, disconnection and errored.
// Its most important methods are `ServeHTTP` which is used to register the server on a specific endpoint
// and `Broadcast` and `Close`.
// Use the `New` function to create a new server, server starts automatically, no further action is required.
type Server struct {
	upgrader    Upgrader
	IDGenerator IDGenerator

	mu         sync.RWMutex
	namespaces Namespaces

	// connection read/write timeouts.
	readTimeout  time.Duration
	writeTimeout time.Duration

	count uint64

	connections map[*Conn]struct{}
	connect     chan *Conn
	disconnect  chan *Conn
	actions     chan action
	broadcaster *broadcaster

	closed uint32

	// OnUpgradeError can be optionally registered to catch upgrade errors.
	OnUpgradeError func(err error)
	// OnConnect can be optionally registered to be notified for any new neffos client connection,
	// it can be used to force-connect a client to a specific namespace(s) or to send data immediately or
	// even to cancel a client connection and dissalow its connection when its return error value is not nil.
	// Don't confuse it with the `OnNamespaceConnect`, this callback is for the entire client side connection.
	OnConnect func(c *Conn) error
	// OnDisconnect can be optionally registered to notify about a connection's disconnect.
	// Don't confuse it with the `OnNamespaceDisconnect`, this callback is for the entire client side connection.
	OnDisconnect func(c *Conn)
}

// New constructs and returns a new neffos server.
// Listens to incoming connections automatically, no further action is required from the caller.
// The second parameter is the "connHandler", it can be
// filled as `Namespaces`, `Events` or `WithTimeout`, same namespaces and events can be used on the client-side as well,
// Use the `Conn#IsClient` on any event callback to determinate if it's a client-side connection or a server-side one.
//
// See examples for more.
func New(upgrader Upgrader, connHandler ConnHandler) *Server {
	readTimeout, writeTimeout := getTimeouts(connHandler)

	s := &Server{
		upgrader:     upgrader,
		namespaces:   connHandler.getNamespaces(),
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		connections:  make(map[*Conn]struct{}),
		connect:      make(chan *Conn, 1),
		disconnect:   make(chan *Conn),
		actions:      make(chan action),
		broadcaster:  newBroadcaster(),
		IDGenerator:  DefaultIDGenerator,
	}

	//	s.broadcastCond = sync.NewCond(&s.broadcastMu)

	go s.start()

	return s
}

func (s *Server) start() {
	atomic.StoreUint32(&s.closed, 0)

	for {
		select {
		case c := <-s.connect:
			s.connections[c] = struct{}{}
			atomic.AddUint64(&s.count, 1)
		case c := <-s.disconnect:
			if _, ok := s.connections[c]; ok {
				// close(c.out)
				delete(s.connections, c)
				atomic.AddUint64(&s.count, ^uint64(0))
				// println("disconnect...")
				if s.OnDisconnect != nil {
					// don't fire disconnect if was immediately closed on the `OnConnect` server event.
					if !c.readiness.isReady() || (c.readiness.err != nil) {
						continue
					}
					s.OnDisconnect(c)
				}
			}
		case act := <-s.actions:
			for c := range s.connections {
				act.call(c)
			}

			if act.done != nil {
				act.done <- struct{}{}
			}
		}
	}
}

// Close terminates the server and all of its connections, client connections are getting notified.
func (s *Server) Close() {
	if atomic.CompareAndSwapUint32(&s.closed, 0, 1) {
		s.Do(func(c *Conn) {
			c.Close()
		}, false)
	}
}

var (
	errServerClosed  = errors.New("server closed")
	errInvalidMethod = errors.New("no valid request method")
)

// Upgrade handles the connection, same as `ServeHTTP` but it can accept
// a socket wrapper and it does return the connection or any errors.
func (s *Server) Upgrade(w http.ResponseWriter, r *http.Request, socketWrapper func(Socket) Socket) (*Conn, error) {
	if atomic.LoadUint32(&s.closed) > 0 {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return nil, errServerClosed
	}

	if r.Method != http.MethodGet {
		// RCF rfc2616 https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
		// The response MUST include an Allow header containing a list of valid methods for the requested resource.
		//
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Allow#Examples
		w.Header().Set("Allow", http.MethodGet)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintln(w, http.StatusText(http.StatusMethodNotAllowed))
		return nil, errInvalidMethod
	}

	socket, err := s.upgrader(w, r)
	if err != nil {
		if s.OnUpgradeError != nil {
			s.OnUpgradeError(err)
		}
		return nil, err
	}

	if socketWrapper != nil {
		socket = socketWrapper(socket)
	}

	c := newConn(socket, s.namespaces)
	c.id = s.IDGenerator(w, r)
	c.readTimeout = s.readTimeout
	c.writeTimeout = s.writeTimeout
	c.server = s
	// TODO: find a way to shutdown this goroutine if not broadcast, or select the other way...
	// DONE: found, see `waitMessage`.
	// go func(c *conn) {
	// 	for {
	// 		// select {
	// 		// case <-c.closeCh:
	// 		// 	return
	// 		// default:
	// 		s.broadcastMu.Lock()
	// 		s.broadcastCond.Wait()
	//
	// 		if s.broadcastMessage.from != c.ID() {
	// 			if !c.write(s.broadcastMessage) && c.IsClosed() {
	// 				s.broadcastMu.Unlock()
	// 				return
	// 			}
	// 		}
	//
	// 		s.broadcastMu.Unlock()
	// 	}

	// }(c)

	go func(c *Conn) {
		for s.waitMessage(c) {
		}
	}(c)

	s.connect <- c

	go c.startReader()
	// Start the reader before `OnConnect`, remember clients may remotely connect to namespace before `Server#OnConnect`
	// therefore any `Server:NSConn#OnNamespaceConnected` can write immediately to the client too.
	// Note also that the `Server#OnConnect` itself can do that as well but if the written Message's Namespace is not locally connected
	// it, correctly, can't pass the write checks. Also, and most important, the `OnConnect` is ready to connect a client to a namespace (locally and remotely).
	//
	// This has a downside:
	// We need a way to check if the `OnConnect` returns an non-nil error which means that the connection should terminate before namespace connect or anything.
	// The solution is to still accept reading messages but add them to the queue(like we already do for any case messages came before ack),
	// the problem to that is that the queue handler is fired when ack is done but `OnConnect` may not even return yet, so we introduce a `mark ready` atomic scope
	// and a channel which will wait for that `mark ready` if handle queue is called before ready.
	// Also make the same check before emit the connection's disconnect event (if defined),
	// which will be always ready to be called because we added the connections via the connect channel;
	// we still need the connection to be available for any broadcasting on connected events.
	// ^ All these only when server-side connection in order to correctly handle the end-developer's `OnConnect`.
	//
	// Look `Conn.serverReadyWaiter#startReader##handleQueue.serverReadyWaiter.unwait`(to hold the events until no error returned or)
	// `#Write:serverReadyWaiter.unwait` (for things like server connect).
	// All cases tested & worked perfectly.
	if s.OnConnect != nil {
		if err = s.OnConnect(c); err != nil {
			// TODO: Do something with that error.
			// The most suitable thing we can do is to somehow send this to the client's `Dial` return statement.
			// This can be done if client waits for "OK" signal or a failure with an error before return the websocket connection,
			// as for today we have the ack process which does NOT block and end-developer can send messages and server will handle them when both sides are ready.
			// So, maybe it's a better solution to transform that process into a blocking state which can handle any `Server#OnConnect` error and return it at client's `Dial`.
			// Think more later today.
			// Done but with a lot of code.... will try to cleanup some things.
			//println("OnConnect error: " + err.Error())
			c.readiness.unwait(err)
			// c.Close()
			return nil, err
		}
	}

	//println("OnConnect does not exist or no error, fire unwait")
	c.readiness.unwait(nil)

	return c, nil
}

// ServeHTTP completes the `http.Handler` interface, it should be passed on a http server's router
// to serve this neffos server on a specific endpoint.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Upgrade(w, r, nil)
}

func (s *Server) waitMessage(c *Conn) bool {
	s.broadcaster.mu.Lock()
	defer s.broadcaster.mu.Unlock()

	msg, ok := s.broadcaster.waitUntilClosed(c.closeCh)
	if !ok {
		return false
	}

	// don't send to its own if set-ed.
	if msg.from == c.ID() {
		return true
	}

	// if "To" field is given then send to a specific connection.
	if msg.To != "" && msg.To != c.ID() {
		return true
	}

	return c.Write(msg) && !c.IsClosed()
}

// GetTotalConnections returns the total amount of the connected connections to the server, it's fast
// and can be used as frequently as needed.
func (s *Server) GetTotalConnections() uint64 {
	return atomic.LoadUint64(&s.count)
}

type action struct {
	call func(*Conn)
	done chan struct{}
}

// Do loops through all connected connections and fires the "fn", with this method
// callers can do whatever they want on a connection outside of a event's callback,
// but make sure that these operations are not taking long time to complete because it delays the
// new incoming connections.
func (s *Server) Do(fn func(*Conn), async bool) {
	act := action{call: fn}
	if !async {
		act.done = make(chan struct{})
		// go func() { s.actions <- act }()
		// <-act.done
	}

	s.actions <- act
	if !async {
		<-act.done
	}
}

type stringerValue struct{ v string }

func (s stringerValue) String() string { return s.v }

// Exclude can be passed on `Server#Broadcast` when
// caller does not have access to the `Conn`, `NSConn` or a `Room` value but
// has access to a string variable which is a connection's ID instead.
//
// Example Code:
// nsConn.Conn.Server().Broadcast(
//	neffos.Exclude("connection_id_here"),
//  neffos.Message{Namespace: "default", Room: "roomName or empty", Event: "chat", Body: [...]})
func Exclude(connID string) fmt.Stringer { return stringerValue{connID} }

// Broadcast method is fast and does not block any new incoming connection,
// it can be used as frequently as needed. Use the "msg"'s Namespace, or/and Event or/and Room to broadcast
// to a specific type of connection collectives.
//
// If first "exceptSender" parameter is not nil then the message "msg" will be
// broadcasted to all connected clients except the given connection's ID,
// any value that completes the `fmt.Stringer` interface is valid. Keep note that
// `Conn`, `NSConn`, `Room` and `Exclude(connID) global function` are valid values.
//
// Example Code:
// nsConn.Conn.Server().Broadcast(
//	nsConn OR nil,
//  neffos.Message{Namespace: "default", Room: "roomName or empty", Event: "chat", Body: [...]})
func (s *Server) Broadcast(exceptSender fmt.Stringer, msg Message) {
	if exceptSender != nil {
		msg.from = exceptSender.String()
	}

	// s.broadcast <- msg

	// s.broadcastMu.Lock()
	// s.broadcastMessage = msg
	// s.broadcastMu.Unlock()

	// s.broadcastCond.Broadcast()

	s.broadcaster.broadcast(msg)
}

// GetConnectionsByNamespace can be used as an alternative way to retrieve
// all connected connections to a specific "namespace" on a specific time point.
// Do not use this function frequently, it is not designed to be fast or cheap, use it for debugging or logging every 'x' time.
// Users should work with the event's callbacks alone, the usability is enough for all type of operations. See `Do` too.
//
// Not thread safe.
func (s *Server) GetConnectionsByNamespace(namespace string) map[string]*NSConn {
	conns := make(map[string]*NSConn)

	s.mu.RLock()
	for c := range s.connections {
		if ns := c.Namespace(namespace); ns != nil {
			conns[ns.Conn.ID()] = ns
		}
	}
	s.mu.RUnlock()

	return conns
}

// GetConnections can be used as an alternative way to retrieve
// all connected connections to the server on a specific time point.
// Do not use this function frequently, it is not designed to be fast or cheap, use it for debugging or logging every 'x' time.
//
// Not thread safe.
func (s *Server) GetConnections() map[string]*Conn {
	conns := make(map[string]*Conn)

	s.mu.RLock()
	for c := range s.connections {
		conns[c.ID()] = c
	}
	s.mu.RUnlock()

	return conns
}

var (
	// ErrBadNamespace may return from a `Conn#Connect` method when the remote side does not declare the given namespace.
	ErrBadNamespace = errors.New("bad namespace")
	// ErrBadRoom may return from a `Room#Leave` method when trying to leave from a not joined room.
	ErrBadRoom = errors.New("bad room")
	// ErrWrite may return from any connection's method when the underline connection is closed (unexpectedly).
	ErrWrite = errors.New("write closed")
)
