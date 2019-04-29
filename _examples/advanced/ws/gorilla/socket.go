package gorilla

import (
	"net"
	"sync"
	"time"

	gorilla "github.com/gorilla/websocket"
)

type Socket struct {
	UnderlyingConn *gorilla.Conn
	client         bool

	mu sync.Mutex
}

func newSocket(underline *gorilla.Conn, client bool) *Socket {
	return &Socket{
		UnderlyingConn: underline,
		client:         client,
	}
}

func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn.UnderlyingConn()
}

func (s *Socket) ReadText(timeout time.Duration) ([]byte, error) {
	for {
		if timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		opCode, data, err := s.UnderlyingConn.ReadMessage()
		if err != nil {
			return nil, err
		}

		if opCode != gorilla.TextMessage {
			// if gorilla.IsUnexpectedCloseError(err, gorilla.CloseGoingAway) ...
			continue
		}

		return data, err
	}
}

func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	if timeout > 0 {
		s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout))
	}

	s.mu.Lock()
	err := s.UnderlyingConn.WriteMessage(gorilla.TextMessage, body)
	s.mu.Unlock()

	return err
}
