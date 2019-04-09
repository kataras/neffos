package ws

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/kataras/fastws"
)

type Server struct {
	mu         sync.RWMutex
	NSAcceptor NSAcceptor
	namespaces Namespaces

	// connections chan *conn
	ws    *fastws.FastWS
	count uint64

	connections map[*conn]struct{}
	connect     chan *conn
	disconnect  chan *conn
	actions     chan func(Conn)
	closed      uint32

	OnError      func(c Conn, err error) bool
	OnConnect    func(c Conn) error
	OnDisconnect func(c Conn)
}

func New(connHandler connHandler) *Server {
	ws := fastws.New()
	s := &Server{
		namespaces: connHandler.getNamespaces(),

		connections: make(map[*conn]struct{}),
		connect:     make(chan *conn, 1),
		disconnect:  make(chan *conn),
		actions:     make(chan func(Conn)),

		// connections: make(chan *conn, 1),
		ws:         ws,
		NSAcceptor: DefaultNSAcceptor,
	}

	ws.OnConnected = s.onConnected
	go s.start()

	return s
}

func (s *Server) SetIDGenerator(gen func(*http.Request) string) {
	if gen == nil {
		s.ws.IDGenerator = fastws.DefaultIDGenerator
	}

	s.ws.IDGenerator = func(c *fastws.Conn) string {
		return gen(c.Request)
	}
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
				delete(s.connections, c)
				// close(c.out)
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
	s.ws.UpgradeHTTP(w, r)
}

func (s *Server) GetTotalConnections() uint64 {
	return atomic.LoadUint64(&s.count)
}

func (s *Server) Do(fn func(Conn)) {
	s.actions <- fn
}

// not thread safe.
func (s *Server) GetConnectionsByNamespace(namespace string) map[string]NSConn {
	conns := make(map[string]NSConn)

	s.mu.RLock()
	for c := range s.connections {
		if ns, ok := c.connectedNamespaces[namespace]; ok {
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

func (s *Server) onConnected(conn *fastws.Conn) error {
	// namespace := conn.Request.URL.Query().Get("ns")
	// if !s.NSAcceptor(conn.Request, namespace) {
	// 	return ErrForbiddenNamespace
	// }

	// events, ok := s.namespaces[namespace]
	// if !ok {
	// 	return ErrBadNamespace
	// }

	c := newConn(conn, s.namespaces)
	c.server = s

	//	nsConn := c.getNSConnection(namespace)

	if s.OnError != nil {
		conn.OnError = func(err error) bool {
			if fastws.IsDisconnected(err) {
				return false
			}

			return s.OnError(c, err)
		}
	}

	s.connect <- c
	go c.startWriter()
	go c.startReader()

	if s.OnConnect != nil {
		if err := s.OnConnect(c); err != nil {
			return err
		}
	}

	// events.fireOnNamespaceConnect(c, Message{Namespace: namespace, isConnect: true})

	return nil
}
