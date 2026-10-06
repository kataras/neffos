package gobwas

import (
	"bytes"
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

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// defaultControlTimeout bounds the write of a control frame when no timeout
// is given: pong and close replies, the close frame sent for an oversized
// message, and Close or Ping called with a zero timeout.
const defaultControlTimeout = time.Second

// Socket completes the `neffos.Socket` interface,
// it describes the underline websocket connection.
//
// It also implements neffos.SocketCloser, neffos.SocketPinger and
// neffos.SocketReadLimiter. Every frame it writes, control frames included,
// goes out under one mutex, so frames never interleave on the wire, and
// nothing is written after its close frame.
type Socket struct {
	UnderlyingConn net.Conn
	request        *http.Request

	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	state          gobwas.State

	mu sync.Mutex

	// readTimeout is the timeout of the ReadData call in progress, so a pong
	// can push the read deadline forward by the same amount.
	readTimeout atomic.Int64
	// readLimit is the largest message ReadData accepts, 0 for no limit.
	readLimit atomic.Int64
	// pong receives a value (without blocking) for every pong frame.
	pong chan struct{}
	// closeSent is set once a close frame has been written. RFC 6455 section
	// 5.5.1 allows nothing after it, so every write path stops here.
	closeSent atomic.Bool
}

// errCloseSent is returned by every write once the close frame went out. It
// wraps net.ErrClosed, so neffos.IsCloseError reports true for it.
var errCloseSent = fmt.Errorf("neffos/gobwas: close frame already sent: %w", net.ErrClosed)

func newSocket(underline net.Conn, request *http.Request, client bool) *Socket {
	state := gobwas.StateServerSide
	if client {
		state = gobwas.StateClientSide
	}

	s := &Socket{
		UnderlyingConn: underline,
		request:        request,
		state:          state,
		pong:           make(chan struct{}, 1),
	}
	s.controlHandler = s.handleControl

	s.reader = &wsutil.Reader{
		Source:          underline,
		State:           state,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		// "intermediate" frames, that possibly could
		// be received between text/binary continuation frames.
		// Read `gobwas/wsutil/reader#NextReader`.
		//
		OnIntermediate: s.controlHandler,
	}

	return s
}

// handleControl handles a ping, pong or close frame. It reads the payload
// (at most 125 bytes) first, then lets gobwas write the reply while holding
// the write mutex, so the reply cannot land inside a data frame that another
// goroutine is writing. A pong also moves the read deadline forward and wakes
// a waiting Ping. A close frame is answered and returned as a
// neffos.CloseError.
func (s *Socket) handleControl(h gobwas.Header, r io.Reader) error {
	payload, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	if h.OpCode == gobwas.OpPong {
		if timeout := time.Duration(s.readTimeout.Load()); timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		select {
		case s.pong <- struct{}{}:
		default:
		}
	}

	s.mu.Lock()
	if s.closeSent.Load() {
		// our own close frame already went out, so gobwas must not write a
		// reply of its own. A close frame still has to be reported.
		s.mu.Unlock()

		if h.OpCode == gobwas.OpClose {
			return closedError(payload)
		}
		return nil
	}

	if h.OpCode == gobwas.OpClose {
		// the handler echoes the close frame back.
		s.closeSent.Store(true)
	}
	s.UnderlyingConn.SetWriteDeadline(time.Now().Add(defaultControlTimeout))
	err = wsutil.ControlFrameHandler(s.UnderlyingConn, s.state)(h, bytes.NewReader(payload))
	s.mu.Unlock()

	if ce, ok := errors.AsType[wsutil.ClosedError](err); ok {
		return neffos.CloseError{Code: int(ce.Code), Reason: ce.Reason}
	}

	return err
}

// closedError reads the code and reason out of a close frame payload.
func closedError(payload []byte) error {
	code, reason := gobwas.StatusNoStatusRcvd, ""
	if len(payload) >= 2 {
		code, reason = gobwas.ParseCloseFrameData(payload)
	}

	return neffos.CloseError{Code: int(code), Reason: reason}
}

// NetConn returns the underline net connection.
func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn
}

// Request returns the http request value.
func (s *Socket) Request() *http.Request {
	return s.request
}

// ReadData reads binary or text messages from the remote connection.
//
// A close frame from the remote side is answered and returned as a
// neffos.CloseError with its code and reason. A message over the read limit
// sends a close frame with neffos.CloseMessageTooBig, drops the rest of the
// input for up to a second, closes the connection and returns
// neffos.ErrMessageTooBig. A pong from the remote side moves the
// read deadline forward by timeout.
func (s *Socket) ReadData(timeout time.Duration) ([]byte, neffos.MessageType, error) {
	s.readTimeout.Store(int64(timeout))

	limit := s.readLimit.Load()
	// MaxFrameSize rejects a big frame from its header alone. It never goes
	// below the control frame size, so pings and close frames always pass.
	// The message length check below covers the rest.
	s.reader.MaxFrameSize = 0
	if limit > 0 {
		s.reader.MaxFrameSize = max(limit, gobwas.MaxControlFramePayloadSize)
	}

	for {
		if timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		hdr, err := s.reader.NextFrame()
		if err != nil {
			if errors.Is(err, wsutil.ErrFrameTooLarge) {
				return s.messageTooBig()
			}
			return nil, 0, mapReadError(err)
		}

		if hdr.OpCode.IsControl() {
			err = s.controlHandler(hdr, s.reader)
			if err != nil {
				return nil, 0, err
			}
			continue
		}

		if hdr.OpCode&gobwas.OpBinary == 0 && hdr.OpCode&gobwas.OpText == 0 {
			err = s.reader.Discard()
			if err != nil {
				return nil, 0, err
			}
			continue
		}

		var src io.Reader = s.reader
		if limit > 0 {
			// one byte over the limit is enough to know it is too big.
			src = io.LimitReader(s.reader, limit+1)
		}

		b, err := io.ReadAll(src)
		if err != nil {
			if errors.Is(err, wsutil.ErrFrameTooLarge) {
				return s.messageTooBig()
			}
			return nil, 0, mapReadError(err)
		}

		if limit > 0 && int64(len(b)) > limit {
			return s.messageTooBig()
		}

		return b, neffos.MessageType(hdr.OpCode), nil
	}
}

// mapReadError reports a connection that ended without a close frame as a
// neffos.CloseError with CloseAbnormalClosure, which is what the gorilla
// adapter does too, so Conn.Err carries code 1006 on both backends. The
// original io.ErrUnexpectedEOF stays in the chain, since that is what this
// adapter used to return. Other errors, timeouts included, pass through.
func mapReadError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		// io.ErrUnexpectedEOF, not io.EOF, so an io.ReadAll over this socket
		// still reports an error when the connection is gone.
		return fmt.Errorf("%w: %w",
			neffos.CloseError{Code: neffos.CloseAbnormalClosure, Reason: io.ErrUnexpectedEOF.Error()},
			io.ErrUnexpectedEOF)
	}

	return err
}

// messageTooBig closes the connection with neffos.CloseMessageTooBig and
// returns neffos.ErrMessageTooBig. The rest of the message is drained first,
// see drainInput.
func (s *Socket) messageTooBig() ([]byte, neffos.MessageType, error) {
	body := gobwas.NewCloseFrameBody(neffos.CloseMessageTooBig, "message too big")
	// best effort: the read already failed, so the close goes ahead whether
	// or not the remote side can still be told why.
	s.writeControl(gobwas.NewCloseFrame(body), defaultControlTimeout)
	drainInput(s.UnderlyingConn)
	s.UnderlyingConn.Close()

	return nil, 0, neffos.ErrMessageTooBig
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
	return s.write(body, gobwas.OpBinary, timeout)
}

// WriteText sends a text message to the remote connection.
func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	return s.write(body, gobwas.OpText, timeout)
}

func (s *Socket) write(body []byte, op gobwas.OpCode, timeout time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// checked under the mutex, so a write can never follow the close frame.
	if s.closeSent.Load() {
		return errCloseSent
	}

	// control frame writes set their own deadline, so always set one here.
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	s.UnderlyingConn.SetWriteDeadline(deadline)

	return wsutil.WriteMessage(s.UnderlyingConn, s.state, op, body)
}

// writeControl writes a control frame under the write mutex, masked when this
// is the client side. timeout bounds the write, one second when it is zero.
// Once a close frame has been written it writes nothing more and returns
// errCloseSent.
func (s *Socket) writeControl(f gobwas.Frame, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultControlTimeout
	}

	if s.state.ClientSide() {
		f = gobwas.MaskFrameInPlace(f)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closeSent.Load() {
		return errCloseSent
	}
	if f.Header.OpCode == gobwas.OpClose {
		// set before the write: a close frame that only went out in part
		// still rules out everything after it.
		s.closeSent.Store(true)
	}

	s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout))
	return gobwas.WriteFrame(s.UnderlyingConn, f)
}

// Close sends a close frame with code and reason, waiting at most timeout for
// the write (one second when timeout is zero), and then closes the
// connection. A failed close frame write does not stop the close. The reason
// should fit in 123 bytes; neffos.Conn trims it before calling Close. It is
// safe to call while other goroutines read and write.
func (s *Socket) Close(code int, reason string, timeout time.Duration) error {
	var body []byte
	if code != neffos.CloseNoStatusReceived {
		body = gobwas.NewCloseFrameBody(gobwas.StatusCode(code), reason)
	}

	s.writeControl(gobwas.NewCloseFrame(body), timeout)
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

	if err := s.writeControl(gobwas.NewPingFrame(nil), timeout); err != nil {
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
		return fmt.Errorf("neffos/gobwas: no pong within %s: %w", timeout, os.ErrDeadlineExceeded)
	}
}

// SetReadLimit sets the largest message, in bytes, ReadData accepts. A bigger
// message closes the connection with neffos.CloseMessageTooBig. n <= 0 removes
// the limit. A ReadData call already in progress keeps the limit it started
// with.
func (s *Socket) SetReadLimit(n int64) {
	if n < 0 {
		n = 0
	}

	s.readLimit.Store(n)
}
