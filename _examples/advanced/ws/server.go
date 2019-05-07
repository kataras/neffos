package ws

import (
	"errors"
	"fmt"
	"log"
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

	connections map[*conn]struct{}
	connect     chan *conn
	disconnect  chan *conn
	actions     chan func(Conn)

	//
	// broadcastMessage Message
	// broadcastMu      sync.Mutex
	// broadcastCond    *sync.Cond
	//	broadcast        chan Message
	// no... currentReceivers map[string]map[*nsConn]struct{}
	broadcaster *broadcaster
	//

	closed uint32

	OnError      func(c Conn, err error) bool
	OnConnect    func(c Conn) error
	OnDisconnect func(c Conn)
}

func New(upgrader Upgrader, connHandler ConnHandler) *Server {
	readTimeout, writeTimeout := getTimeouts(connHandler)

	s := &Server{
		upgrader:     upgrader,
		namespaces:   connHandler.getNamespaces(),
		readTimeout:  readTimeout,
		writeTimeout: writeTimeout,
		connections:  make(map[*conn]struct{}),
		connect:      make(chan *conn, 1),
		disconnect:   make(chan *conn),
		actions:      make(chan func(Conn)),
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
		s.Do(func(c Conn) {
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
		log.Printf("Upgrade: %v", err)
		return
	}

	c := newConn(socket, s.namespaces)
	c.id = s.IDGenerator(w, r)
	c.readTimeout = s.readTimeout
	c.writeTimeout = s.writeTimeout
	c.server = s

	go c.startReader()
	// go c.startWriter()

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

	go func(c *conn) {
		for s.waitMessage(c) {
		}
	}(c)

	s.connect <- c

	if s.OnConnect != nil {
		if err = s.OnConnect(c); err != nil {
			s.disconnect <- c
		}
	}
}

func (s *Server) waitMessage(c *conn) bool {
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

func (s *Server) Do(fn func(Conn)) {
	s.actions <- fn
}

func (s *Server) Broadcast(from Conn, msg Message) {
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
func (s *Server) GetConnectionsByNamespace(namespace string) map[string]NSConn {
	conns := make(map[string]NSConn)

	s.mu.RLock()
	for c := range s.connections {
		if ns := c.connectedNamespaces.get(namespace); ns != nil {
			conns[ns.ID()] = ns
		}
	}
	s.mu.RUnlock()

	return conns
}

// not thread safe.
func (s *Server) GetConnections() map[string]Conn {
	conns := make(map[string]Conn)

	s.mu.RLock()
	for c := range s.connections {
		conns[c.ID()] = c
	}
	s.mu.RUnlock()

	return conns
}

var ErrBadNamespace = errors.New("bad namespace")
var ErrForbiddenNamespace = errors.New("forbidden namespace")
