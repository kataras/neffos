package coder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"

	"github.com/coder/websocket"
)

var (
	_ neffos.Socket            = (*Socket)(nil)
	_ neffos.SocketCloser      = (*Socket)(nil)
	_ neffos.SocketPinger      = (*Socket)(nil)
	_ neffos.SocketReadLimiter = (*Socket)(nil)
)

// defaultControlTimeout bounds Close and a zero-timeout Ping, and the close
// handshake after an oversized message.
const defaultControlTimeout = time.Second

// Socket completes the `neffos.Socket` interface,
// it describes the underline websocket connection.
//
// It also implements neffos.SocketCloser, neffos.SocketPinger and
// neffos.SocketReadLimiter. coder/websocket serialises writes itself, so the
// socket adds no lock around them, and coder writes nothing after its close
// frame.
type Socket struct {
	UnderlyingConn *websocket.Conn
	request        *http.Request

	// ctx is the context of every read. Close cancels it when the peer does
	// not answer in time, and coder then drops the transport under a read
	// that is in flight.
	ctx    context.Context
	cancel context.CancelFunc

	// timerMu guards readTimer and readTimeout, which the pong handler uses
	// to give a ReadData in flight more time.
	timerMu     sync.Mutex
	readTimer   *time.Timer
	readTimeout time.Duration

	// limitMu guards readLimit, the limit SetReadLimit was given (-1 for
	// none), and netConn, which is made once.
	limitMu   sync.Mutex
	readLimit int64
	netConn   net.Conn
}

func newSocket(request *http.Request) *Socket {
	ctx, cancel := context.WithCancel(context.Background())
	return &Socket{
		request:   request,
		ctx:       ctx,
		cancel:    cancel,
		readLimit: -1,
	}
}

// init stores the connection once the handshake is done. coder limits
// messages to 32 KB by default; neffos has no limit unless MaxMessageSize is
// set, so the limit goes off here.
func (s *Socket) init(underline *websocket.Conn) {
	s.UnderlyingConn = underline
	underline.SetReadLimit(-1)
}

// pongHandler returns the OnPongReceived callback: the caller's own one, if
// any, and then a reset of the read timer of the ReadData in flight.
func (s *Socket) pongHandler(user func(context.Context, []byte)) func(context.Context, []byte) {
	return func(ctx context.Context, payload []byte) {
		if user != nil {
			user(ctx, payload)
		}

		s.timerMu.Lock()
		// Stop fails when the timer already fired, and then the read is
		// over: leave it.
		if s.readTimer != nil && s.readTimer.Stop() {
			s.readTimer.Reset(s.readTimeout)
		}
		s.timerMu.Unlock()
	}
}

// netConn is coder's net.Conn view of the connection with Close replaced:
// coder's own Close runs the close handshake and can wait up to 5s for the
// remote side.
type netConn struct {
	net.Conn
	ws *websocket.Conn
}

// Close closes the connection at once, with no close frame.
func (c netConn) Close() error {
	return c.ws.CloseNow()
}

// NetConn returns the connection as a net.Conn, made with websocket.NetConn
// on first use. Its Close closes the connection at once, with no close
// handshake. On the client side its RemoteAddr and LocalAddr are coder's
// placeholder address, since coder does not expose the dialed net.Conn.
func (s *Socket) NetConn() net.Conn {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()

	if s.netConn == nil {
		nc := websocket.NetConn(context.Background(), s.UnderlyingConn, websocket.MessageBinary)
		// websocket.NetConn turns the read limit off; put back the one
		// SetReadLimit set.
		s.UnderlyingConn.SetReadLimit(s.readLimit)
		s.netConn = netConn{Conn: nc, ws: s.UnderlyingConn}
	}

	return s.netConn
}

// Request returns the http request value. It is nil on the client side.
func (s *Socket) Request() *http.Request {
	return s.request
}

// ReadData reads binary or text messages from the remote connection.
//
// With a timeout above zero and no message in time, the connection is closed
// and the error matches os.ErrDeadlineExceeded. A pong from the remote side
// restarts that timeout. A close frame from the remote side is returned as a
// neffos.CloseError with its code and reason, and a connection that ends
// without one as a neffos.CloseError with neffos.CloseAbnormalClosure. A
// message over the read limit returns an error that matches
// neffos.ErrMessageTooBig, after coder sent a close frame with
// neffos.CloseMessageTooBig and the remote side answered it, or a second
// passed. That close handshake runs inside the goroutine that called
// ReadData, so against a remote side that never answers, ReadData takes about
// a second longer to return.
func (s *Socket) ReadData(timeout time.Duration) ([]byte, neffos.MessageType, error) {
	var timedOut atomic.Bool
	if timeout > 0 {
		s.timerMu.Lock()
		s.readTimeout = timeout
		s.readTimer = time.AfterFunc(timeout, func() {
			timedOut.Store(true)
			s.UnderlyingConn.CloseNow()
		})
		s.timerMu.Unlock()

		defer func() {
			s.timerMu.Lock()
			s.readTimer.Stop()
			s.readTimer = nil
			s.timerMu.Unlock()
		}()
	}

	typ, data, err := s.UnderlyingConn.Read(s.ctx)
	if err != nil {
		if timedOut.Load() {
			return nil, 0, fmt.Errorf("neffos/coder: no message within %s: %w", timeout, os.ErrDeadlineExceeded)
		}
		return nil, 0, s.mapReadError(err)
	}

	return data, neffos.MessageType(typ), nil
}

// mapReadError turns coder's close, read limit and EOF errors into the neffos
// ones. Other errors pass through unchanged.
func (s *Socket) mapReadError(err error) error {
	if ce, ok := errors.AsType[websocket.CloseError](err); ok {
		return neffos.CloseError{Code: int(ce.Code), Reason: ce.Reason}
	}

	if errors.Is(err, websocket.ErrMessageTooBig) {
		// coder has sent the close frame with CloseMessageTooBig but left
		// the connection open. Closing it now, with the rest of the message
		// unread, would reset the TCP connection, and the remote side could
		// lose the close frame. coder's close handshake reads and drops
		// input until the remote side answers, so run it, bounded.
		s.Close(neffos.CloseMessageTooBig, "", defaultControlTimeout)
		return fmt.Errorf("%w: %v", neffos.ErrMessageTooBig, err)
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// the connection ended with no close frame. Keep err in the chain.
		closeErr := neffos.CloseError{Code: neffos.CloseAbnormalClosure, Reason: io.ErrUnexpectedEOF.Error()}
		return fmt.Errorf("%w: %w", closeErr, err)
	}

	return err
}

// WriteBinary sends a binary message to the remote connection. With a
// timeout above zero, a write that takes longer closes the connection.
func (s *Socket) WriteBinary(body []byte, timeout time.Duration) error {
	return s.write(websocket.MessageBinary, body, timeout)
}

// WriteText sends a text message to the remote connection. With a timeout
// above zero, a write that takes longer closes the connection.
func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	return s.write(websocket.MessageText, body, timeout)
}

func (s *Socket) write(typ websocket.MessageType, body []byte, timeout time.Duration) error {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	return s.UnderlyingConn.Write(ctx, typ, body)
}

// Close sends a close frame with code and reason and waits up to timeout (one
// second when timeout is zero) for the remote side to answer, then closes the
// connection. It returns nil when the remote side answered with a close
// frame, or when it did not answer in time. Every later write fails. It is
// safe to call while other goroutines read and write.
//
// When the remote side does not answer, Close still returns after timeout,
// but the transport can stay open in the background for up to coder's fixed
// 5s when no ReadData holds the read lock. That happens in two cases: no
// ReadData is running at all, or Close ran between two reads. In the second
// case coder's close handshake takes the read lock first, so the next
// ReadData fails with "failed to acquire lock: context canceled" and does not
// close the transport itself.
func (s *Socket) Close(code int, reason string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultControlTimeout
	}

	done := make(chan error, 1)
	go func() {
		done <- s.UnderlyingConn.Close(websocket.StatusCode(code), reason)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		if websocket.CloseStatus(err) != -1 {
			// the remote side answered with a different close frame.
			return nil
		}
		return err
	case <-timer.C:
	}

	// coder's CloseNow waits for a close handshake in progress, so it cannot
	// cut it short. Cancelling the read context makes coder drop the
	// transport under a ReadData in flight, which neffos always has.
	s.cancel()
	go s.UnderlyingConn.CloseNow()
	return nil
}

// Ping sends a ping frame. With a timeout above zero it then waits up to that
// long for the pong and returns an error that matches os.ErrDeadlineExceeded
// when none arrives. With a zero timeout it returns at once and the ping goes
// out in the background. Pongs are only seen while another goroutine is
// inside ReadData. Ping(0) leaves a goroutine running for up to a second,
// because coder's Ping always waits for the pong.
func (s *Socket) Ping(timeout time.Duration) error {
	if timeout <= 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), defaultControlTimeout)
			defer cancel()
			s.UnderlyingConn.Ping(ctx)
		}()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := s.UnderlyingConn.Ping(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("neffos/coder: no pong within %s: %w", timeout, os.ErrDeadlineExceeded)
	}
	return err
}

// SetReadLimit sets the largest message, in bytes, ReadData accepts. A bigger
// message closes the connection with neffos.CloseMessageTooBig. n <= 0 removes
// the limit. It applies from the next message on.
func (s *Socket) SetReadLimit(n int64) {
	if n <= 0 {
		n = -1
	}

	s.limitMu.Lock()
	s.readLimit = n
	s.UnderlyingConn.SetReadLimit(n)
	s.limitMu.Unlock()
}
