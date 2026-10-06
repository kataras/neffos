package gorilla

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"

	gorilla "github.com/gorilla/websocket"
)

// defaultControlTimeout bounds a close or ping frame write when Close or Ping
// is called with a zero timeout.
const defaultControlTimeout = time.Second

// Socket completes the `neffos.Socket` interface,
// it describes the underline websocket connection.
//
// It also implements neffos.SocketCloser, neffos.SocketPinger and
// neffos.SocketReadLimiter.
type Socket struct {
	UnderlyingConn *gorilla.Conn
	request        *http.Request

	client bool

	mu sync.Mutex

	// readTimeout is the timeout of the ReadData call in progress, so the
	// pong handler can push the read deadline forward by the same amount.
	readTimeout atomic.Int64
	// pong receives a value (without blocking) for every pong frame.
	pong chan struct{}
}

func newSocket(underline *gorilla.Conn, request *http.Request, client bool) *Socket {
	s := &Socket{
		UnderlyingConn: underline,
		request:        request,
		client:         client,
		pong:           make(chan struct{}, 1),
	}

	// The pong handler runs inside ReadMessage, on the reader goroutine.
	underline.SetPongHandler(func(string) error {
		if timeout := time.Duration(s.readTimeout.Load()); timeout > 0 {
			underline.SetReadDeadline(time.Now().Add(timeout))
		}

		select {
		case s.pong <- struct{}{}:
		default:
		}
		return nil
	})

	return s
}

// NetConn returns the underline net connection.
func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn.NetConn()
}

// Request returns the http request value.
func (s *Socket) Request() *http.Request {
	return s.request
}

// ReadData reads binary or text messages from the remote connection.
//
// A close frame from the remote side is returned as a neffos.CloseError with
// its code and reason. A message over the read limit returns an error that
// matches neffos.ErrMessageTooBig, after a close frame with
// neffos.CloseMessageTooBig was sent and the rest of the input was dropped for
// up to a second. A pong from the remote side moves the
// read deadline forward by timeout.
func (s *Socket) ReadData(timeout time.Duration) ([]byte, neffos.MessageType, error) {
	s.readTimeout.Store(int64(timeout))

	for {
		if timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		opCode, data, err := s.UnderlyingConn.ReadMessage()
		if err != nil {
			if errors.Is(err, gorilla.ErrReadLimit) {
				drainInput(s.UnderlyingConn.NetConn())
			}
			return nil, 0, mapReadError(err)
		}

		if opCode != gorilla.BinaryMessage && opCode != gorilla.TextMessage {
			continue
		}

		return data, neffos.MessageType(opCode), err
	}
}

// mapReadError turns gorilla's close and read limit errors into the neffos
// ones. Other errors, timeouts included, pass through unchanged.
func mapReadError(err error) error {
	var ce *gorilla.CloseError
	if errors.As(err, &ce) {
		return neffos.CloseError{Code: ce.Code, Reason: ce.Text}
	}

	if errors.Is(err, gorilla.ErrReadLimit) {
		// gorilla has already sent the close frame with CloseMessageTooBig.
		return fmt.Errorf("%w: %v", neffos.ErrMessageTooBig, err)
	}

	return err
}

const (
	drainIdle = 50 * time.Millisecond
	drainMax  = time.Second
)

// drainInput reads and drops what the remote side still sends, until it
// stops for drainIdle, closes the connection, or drainMax passes. Closing a
// TCP connection that still has unread input sends a reset instead of a
// normal close, and the reset can make the remote side drop the close frame
// it has not read yet. Only the reading goroutine may call it.
func drainInput(conn net.Conn) {
	buf := make([]byte, 4096)
	end := time.Now().Add(drainMax)
	for {
		deadline := time.Now().Add(drainIdle)
		if deadline.After(end) {
			deadline = end
		}
		conn.SetReadDeadline(deadline)

		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

// WriteBinary sends a binary message to the remote connection.
func (s *Socket) WriteBinary(body []byte, timeout time.Duration) error {
	return s.write(body, gorilla.BinaryMessage, timeout)
}

// WriteText sends a text message to the remote connection.
func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	return s.write(body, gorilla.TextMessage, timeout)
}

func (s *Socket) write(body []byte, opCode int, timeout time.Duration) error {
	s.mu.Lock()
	// always set a deadline: gorilla stores the last one and reapplies it, so
	// one left behind by an earlier timed write would cut this one short.
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	s.UnderlyingConn.SetWriteDeadline(deadline)

	err := s.UnderlyingConn.WriteMessage(opCode, body)
	s.mu.Unlock()

	return err
}

// Close sends a close frame with code and reason, waiting at most timeout for
// the write (one second when timeout is zero), and then closes the
// connection. A failed close frame write does
// not stop the close. It is safe to call while other goroutines read and
// write.
func (s *Socket) Close(code int, reason string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultControlTimeout
	}

	// WriteControl may be called concurrently with the other methods. The
	// error is dropped on purpose, best effort: the close goes ahead whether
	// or not the remote side can still be told why. gorilla fails every
	// further write with ErrCloseSent after it.
	s.UnderlyingConn.WriteControl(gorilla.CloseMessage, gorilla.FormatCloseMessage(code, reason), time.Now().Add(timeout))
	return s.UnderlyingConn.Close()
}

// Ping sends a ping frame. With a timeout above zero it then waits up to that
// long for a pong and returns os.ErrDeadlineExceeded when none arrives. With a
// zero timeout it returns right after the write. Pongs are only seen while
// another goroutine is inside ReadData.
func (s *Socket) Ping(timeout time.Duration) error {
	// drop a pong left over from an earlier ping.
	select {
	case <-s.pong:
	default:
	}

	writeTimeout := timeout
	if writeTimeout <= 0 {
		writeTimeout = defaultControlTimeout
	}

	if err := s.UnderlyingConn.WriteControl(gorilla.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
		return err
	}

	if timeout <= 0 {
		return nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-s.pong:
		return nil
	case <-timer.C:
		return fmt.Errorf("neffos/gorilla: no pong within %s: %w", timeout, os.ErrDeadlineExceeded)
	}
}

// SetReadLimit sets the largest message, in bytes, ReadData accepts. A bigger
// message closes the connection with neffos.CloseMessageTooBig. n <= 0 removes
// the limit.
func (s *Socket) SetReadLimit(n int64) {
	if n < 0 {
		n = 0
	}

	s.UnderlyingConn.SetReadLimit(n)
}
