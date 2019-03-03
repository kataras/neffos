package ws

import (
	"net/http"
	"sync/atomic"

	"github.com/kataras/fastws"
)

type Server struct {
	rooms map[string]*Room
	// connections chan *conn
	ws    *fastws.FastWS
	count uint64

	connections map[*conn]struct{}
	connect     chan *conn
	disconnect  chan *conn
	broadcast   chan []byte
}

func New() *Server {
	ws := fastws.New()
	s := &Server{
		rooms:       make(map[string]*Room),
		connections: make(map[*conn]struct{}),
		connect:     make(chan *conn),
		disconnect:  make(chan *conn),
		// connections: make(chan *conn, 1),
		ws: ws,
	}

	ws.OnConnected = s.onConnected
	ws.OnError = func(conn *fastws.Conn) bool {
		// TODO: for testing.
		// if !fastws.IsDisconnected(conn.Err()) {
		// 	println("[DEBUG] ws.OnError: " + conn.Err().Error())
		// }

		return false
	}

	go s.start()

	return s
}

func (s *Server) start() {
	for {
		select {
		case c := <-s.connect:
			s.connections[c] = struct{}{}
			atomic.AddUint64(&s.count, 1)
		case c := <-s.disconnect:
			if _, ok := s.connections[c]; ok {
				delete(s.connections, c)
				close(c.out)
				atomic.AddUint64(&s.count, ^uint64(0))
			}
		case b := <-s.broadcast:
			for c := range s.connections {
				select {
				case c.out <- b:
				default:
					// c.leaveAll()
					// close(c.out)
					// close(c.closeCh)
					// c.underline.NetConn.Close()
					close(c.out)
					delete(s.connections, c)
					atomic.AddUint64(&s.count, ^uint64(0))
				}
			}
		}
	}
}

func (s *Server) getRoom(name string) *Room {
	room, ok := s.rooms[name]
	if !ok {
		room = newRoom()
		s.rooms[name] = room
	}

	return room
}

func (s *Server) OnJoin(room string, cb func(Conn) error) {
	r := s.getRoom(room)
	r.OnJoin = cb
}

func (s *Server) OnLeave(room string, cb func(Conn)) {
	r := s.getRoom(room)
	r.OnLeave = cb
}

func (s *Server) OnError(room string, cb func(Conn, error)) {
	r := s.getRoom(room)
	r.OnError = cb
}

func (s *Server) On(room, event string, cb EventListener) {
	r := s.getRoom(room)
	r.On(event, cb)
}

func (s *Server) Close() error {
	// TODO:
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.ws.UpgradeHTTP(w, r)
}

func (s *Server) GetTotalConnections() uint64 {
	return atomic.LoadUint64(&s.count)
}

func (s *Server) onConnected(conn *fastws.Conn) error {
	c := newConn(conn, s.rooms)
	c.server = s

	// s.connections <- c
	room := conn.Request.URL.Query().Get("ns")

	c.wrapConnRoom(room)
	s.connect <- c
	go c.startWriter()
	go c.startReader()
	return nil
}

// func (s *Server) Start() {
// 	for {
// 		c := <-s.connections
// 		go c.serve()
// 	}
// }
