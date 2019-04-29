package ws

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/fastws"
)

type Server struct {
	upgrader    Upgrader
	IDGenerator IDGenerator

	mu         sync.RWMutex
	NSAcceptor NSAcceptor
	namespaces Namespaces

	// connection read/write timeouts.
	readTimeout  time.Duration
	writeTimeout time.Duration

	// connections chan *conn
	ws    *fastws.FastWS
	count uint64

	connections map[*conn]struct{}
	connect     chan *conn
	disconnect  chan *conn
	actions     chan func(Conn)

	//
	// broadcastMessage Message
	// broadcastMu      sync.Mutex
	// broadcastCond    *sync.Cond
	broadcast chan Message
	// no... currentReceivers map[string]map[*nsConn]struct{}
	//

	closed uint32

	OnError      func(c Conn, err error) bool
	OnConnect    func(c Conn) error
	OnDisconnect func(c Conn)
}

func New(upgrader Upgrader, connHandler connHandler) *Server {
	ws := fastws.New()

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
		broadcast:    make(chan Message, 1),
		// connections: make(chan *conn, 1),
		ws:          ws,
		NSAcceptor:  DefaultNSAcceptor,
		IDGenerator: DefaultIDGenerator,
	}

	// s.broadcastCond = sync.NewCond(&s.broadcastMu)

	ws.OnConnected = s.onConnected
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
		case msg := <-s.broadcast:
			// msgBinary := serializeMessage(nil, msg)
			for c := range s.connections {
				if !c.isAcknowledged() {
					continue
				}

				//	println("send")

				if c.ID() == msg.from {
					continue
				}

				if !msg.isConnect && !msg.isDisconnect {
					c.mu.RLock()
					_, ok := c.connectedNamespaces[msg.Namespace]
					c.mu.RUnlock()
					if !ok {
						continue
					}
				}

				if !c.write(msg) {
					delete(s.connections, c)
					atomic.AddUint64(&s.count, ^uint64(0))
					if s.OnDisconnect != nil {
						s.OnDisconnect(c)
					}
				}
				// select {
				// case c.out <- msgBinary:
				// default:
				// 	// close(c.out)
				// 	delete(s.connections, c)
				// 	atomic.AddUint64(&s.count, ^uint64(0))
				// 	if s.OnDisconnect != nil {
				// 		s.OnDisconnect(c)
				// 	}
				// }
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
		println("method not GET")
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
	c.server = s
	c.ReadTimeout = s.readTimeout
	c.WriteTimeout = s.writeTimeout

	s.connect <- c

	go c.startReader()
	go c.startWriter()

	if s.OnConnect != nil {
		if err = s.OnConnect(c); err != nil {
			s.disconnect <- c
		}
	}
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

	s.broadcast <- msg

	// s.broadcastMu.Lock()
	// s.broadcastMessage = msg
	// s.broadcastMu.Unlock()

	// s.broadcastCond.Broadcast()
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

func (s *Server) onConnected(underline *fastws.Conn) error {
	// namespace := underline.Request.URL.Query().Get("ns")
	// if !s.NSAcceptor(conn.Request, namespace) {
	// 	return ErrForbiddenNamespace
	// }

	// events, ok := s.namespaces[namespace]
	// if !ok {
	// 	return ErrBadNamespace
	// }

	underline.WriteTimeout = s.writeTimeout
	underline.ReadTimeout = s.readTimeout
	// underline.Flush = false

	c := newConn(underline, s.namespaces)
	c.server = s

	if s.OnError != nil {
		underline.OnError = func(err error) bool {
			if fastws.IsDisconnected(err) {
				return false
			}

			return s.OnError(c, err)
		}
	}

	// if err := c.ack(); err != nil {
	// 	return err
	// }

	//	nsConn := c.getNSConnection(namespace)

	s.connect <- c

	go c.startReader()
	go c.startWriter()

	// go func(c *conn) {
	// 	for {
	// 		s.broadcastMu.Lock()
	// 		s.broadcastCond.Wait()
	// 		if c == nil || c.isClosed() {
	// 			s.broadcastMu.Unlock()
	// 			return
	// 		}

	// 		if s.broadcastMessage.from != c.ID() {
	// 			c.write(s.broadcastMessage)
	// 		}

	// 		s.broadcastMu.Unlock()
	// 	}
	// }(c)

	if s.OnConnect != nil {
		if err := s.OnConnect(c); err != nil {
			return err
		}
	}

	// events.fireOnNamespaceConnect(c, Message{Namespace: namespace, isConnect: true})

	return nil
}
