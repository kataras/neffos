package ws

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
	actions     chan func(*Conn)
	broadcaster *broadcaster

	closed uint32

	OnUpgradeError func(err error)
	OnConnect      func(c *Conn) error
	OnDisconnect   func(c *Conn)
}

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
		actions:      make(chan func(*Conn)),
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
				if s.OnDisconnect != nil {
					// don't fire disconnect if was immediately closed on the `OnConnect` server event.
					if !c.serverReadyWaiter.isReady() {
						continue
					}
					s.OnDisconnect(c)
				}
			}
		case fn := <-s.actions:
			for c := range s.connections {
				fn(c)
			}
		}
	}
}

func (s *Server) Close() {
	if atomic.CompareAndSwapUint32(&s.closed, 0, 1) {
		s.Do(func(c *Conn) {
			c.Close()
		})
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadUint32(&s.closed) > 0 {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
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
		return
	}

	socket, err := s.upgrader(w, r)
	if err != nil {
		if s.OnUpgradeError != nil {
			s.OnUpgradeError(err)
		}
		return
	}

	c := newConn(socket, s.namespaces)
	c.id = s.IDGenerator(w, r)
	c.readTimeout = s.readTimeout
	c.writeTimeout = s.writeTimeout
	c.server = s
	c.serverReadyWaiter = newWaiter()
	go c.startReader()

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
		if err := s.OnConnect(c); err != nil {
			// TODO: Do something with that error.
			// The most suitable thing we can do is to somehow send this to the client's `Dial` return statement.
			// This can be done if client waits for "OK" signal or a failure with an error before return the websocket connection,
			// as for today we have the ack process which does NOT block and end-developer can send messages and server will handle them when both sides are ready.
			// So, maybe it's a better solution to transform that process into a blocking state which can handle any `Server#OnConnect` error and return it at client's `Dial`.
			// Think more later today.
			c.Close()
			return
		}
	}

	c.serverReadyWaiter.unwait()
}

func (s *Server) waitMessage(c *Conn) bool {
	s.broadcaster.mu.Lock()
	defer s.broadcaster.mu.Unlock()

	msg, ok := s.broadcaster.waitUntilClosed(c.closeCh)
	if !ok {
		return false
	}

	if msg.from != c.ID() {
		if !c.Write(msg) && c.IsClosed() {
			return false
		}
	}

	return true
}

func (s *Server) GetTotalConnections() uint64 {
	return atomic.LoadUint64(&s.count)
}

func (s *Server) Do(fn func(*Conn)) {
	s.actions <- fn
}

func (s *Server) Broadcast(from *Conn, msg Message) {
	if from != nil {
		msg.from = from.ID()
	}

	// s.broadcast <- msg

	// s.broadcastMu.Lock()
	// s.broadcastMessage = msg
	// s.broadcastMu.Unlock()

	// s.broadcastCond.Broadcast()

	s.broadcaster.broadcast(msg)
}

// not thread safe.
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

// not thread safe.
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
	ErrBadNamespace = errors.New("bad namespace")
	ErrBadRoom      = errors.New("bad room")
	ErrWrite        = errors.New("write closed")
)
