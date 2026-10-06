package neffos

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTimeout bounds every wait in the fake-socket tests so a regression fails
// the test instead of hanging the binary.
const fakeTimeout = 2 * time.Second

// fakeSocket is an in-memory Socket for Conn unit tests. Frames pushed to in
// are returned by ReadData; frames written by the Conn land on out. It counts
// calls and records the highest number of concurrent ReadData calls, so tests
// can prove that only one goroutine reads the socket at a time.
type fakeSocket struct {
	in  chan []byte
	out chan []byte

	closeOnce sync.Once
	closed    chan struct{}

	readCalls     atomic.Int32
	writeCalls    atomic.Int32
	activeReads   atomic.Int32
	maxConcurrent atomic.Int32

	// readErr makes the next ReadData return its error, the way an adapter
	// reports a close frame or an oversized message from the remote side.
	readErr chan error

	// optional interfaces: SocketCloser, SocketPinger, SocketReadLimiter.
	mu             sync.Mutex
	closeCode      int
	closeReason    string
	pingTimeout    time.Duration
	pingErr        error
	writeErr       error
	closerCalls    atomic.Int32
	netCloseCalls  atomic.Int32
	pingCalls      atomic.Int32
	readLimit      atomic.Int64
	readLimitCalls atomic.Int32

	req *http.Request
}

var (
	_ Socket            = (*fakeSocket)(nil)
	_ SocketCloser      = (*fakeSocket)(nil)
	_ SocketPinger      = (*fakeSocket)(nil)
	_ SocketReadLimiter = (*fakeSocket)(nil)
)

func newFakeSocket() *fakeSocket {
	return &fakeSocket{
		in:      make(chan []byte, 64),
		out:     make(chan []byte, 64),
		closed:  make(chan struct{}),
		readErr: make(chan error, 1),
		req:     &http.Request{Header: http.Header{}},
	}
}

// Close implements SocketCloser: it records the close frame and closes the socket.
func (s *fakeSocket) Close(code int, reason string, _ time.Duration) error {
	s.closerCalls.Add(1)
	s.mu.Lock()
	s.closeCode, s.closeReason = code, reason
	s.mu.Unlock()
	s.close()
	return nil
}

// lastClose returns the code and reason of the last SocketCloser.Close call.
func (s *fakeSocket) lastClose() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCode, s.closeReason
}

// Ping implements SocketPinger. It fails with the error set by setPingErr.
func (s *fakeSocket) Ping(timeout time.Duration) error {
	s.pingCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingTimeout = timeout
	return s.pingErr
}

func (s *fakeSocket) setPingErr(err error) {
	s.mu.Lock()
	s.pingErr = err
	s.mu.Unlock()
}

func (s *fakeSocket) lastPingTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pingTimeout
}

// SetReadLimit implements SocketReadLimiter.
func (s *fakeSocket) SetReadLimit(n int64) {
	s.readLimitCalls.Add(1)
	s.readLimit.Store(n)
}

// setWriteErr makes every write fail with err until it is set back to nil.
func (s *fakeSocket) setWriteErr(err error) {
	s.mu.Lock()
	s.writeErr = err
	s.mu.Unlock()
}

// remoteClose makes the pending or next ReadData return err.
func (s *fakeSocket) remoteClose(t *testing.T, err error) {
	t.Helper()
	select {
	case s.readErr <- err:
	case <-time.After(fakeTimeout):
		t.Fatal("fake socket: remoteClose timed out")
	}
}

func (s *fakeSocket) NetConn() net.Conn      { return &fakeNetConn{s: s} }
func (s *fakeSocket) Request() *http.Request { return s.req }

func (s *fakeSocket) ReadData(time.Duration) ([]byte, MessageType, error) {
	s.readCalls.Add(1)
	n := s.activeReads.Add(1)
	defer s.activeReads.Add(-1)
	for {
		max := s.maxConcurrent.Load()
		if n <= max || s.maxConcurrent.CompareAndSwap(max, n) {
			break
		}
	}

	select {
	case b := <-s.in:
		return b, TextMessage, nil
	case err := <-s.readErr:
		return nil, 0, err
	case <-s.closed:
		return nil, 0, io.EOF
	}
}

func (s *fakeSocket) WriteBinary(b []byte, timeout time.Duration) error {
	return s.WriteText(b, timeout)
}

func (s *fakeSocket) WriteText(b []byte, _ time.Duration) error {
	s.writeCalls.Add(1)
	select {
	case <-s.closed:
		return io.EOF
	default:
	}

	s.mu.Lock()
	writeErr := s.writeErr
	s.mu.Unlock()
	if writeErr != nil {
		return writeErr
	}

	select {
	case s.out <- bytes.Clone(b):
		return nil
	case <-s.closed:
		return io.EOF
	}
}

func (s *fakeSocket) close() {
	s.closeOnce.Do(func() { close(s.closed) })
}

// push queues a frame for the Conn's next read.
func (s *fakeSocket) push(t *testing.T, b []byte) {
	t.Helper()
	select {
	case s.in <- b:
	case <-time.After(fakeTimeout):
		t.Fatal("fake socket: push timed out")
	}
}

// next returns the next frame the Conn wrote.
func (s *fakeSocket) next(t *testing.T) []byte {
	t.Helper()
	select {
	case b := <-s.out:
		return b
	case <-time.After(fakeTimeout):
		t.Fatal("fake socket: no frame written in time")
		return nil
	}
}

// nextMessage returns the next written frame as a Message. The wait token is
// kept exactly as it travelled on the wire.
func (s *fakeSocket) nextMessage(t *testing.T) Message {
	t.Helper()
	return deserializeMessage(TextMessage, s.next(t), false, false, false)
}

// answerNext reads the next written frame, which must carry a wait token, and
// pushes back an empty reply for it, the way a remote neffos peer acknowledges
// connect, disconnect, join and leave requests. It returns the frame it read.
func (s *fakeSocket) answerNext(t *testing.T) Message {
	t.Helper()
	msg := s.nextMessage(t)
	if msg.wait == "" {
		t.Fatalf("fake socket: expected a frame with a wait token, got %q", serializeMessage(msg))
	}
	s.push(t, genEmptyReplyToWait(msg.wait))
	return msg
}

// fakeNetConn is the net.Conn returned by fakeSocket.NetConn. Only Close is
// used by Conn; the other methods come from the nil embedded interface and
// panic if called.
type fakeNetConn struct {
	net.Conn
	s *fakeSocket
}

func (c *fakeNetConn) Close() error {
	c.s.netCloseCalls.Add(1)
	c.s.close()
	return nil
}

// fakeSocketBare exposes only the Socket methods of a fakeSocket, for tests
// that need a socket without any optional interface.
type fakeSocketBare struct {
	s *fakeSocket
}

var _ Socket = fakeSocketBare{}

func (b fakeSocketBare) NetConn() net.Conn      { return b.s.NetConn() }
func (b fakeSocketBare) Request() *http.Request { return b.s.Request() }
func (b fakeSocketBare) ReadData(timeout time.Duration) ([]byte, MessageType, error) {
	return b.s.ReadData(timeout)
}
func (b fakeSocketBare) WriteBinary(body []byte, timeout time.Duration) error {
	return b.s.WriteBinary(body, timeout)
}
func (b fakeSocketBare) WriteText(body []byte, timeout time.Duration) error {
	return b.s.WriteText(body, timeout)
}

const fakeNamespace = "default"

// newFakeClientConn builds a client-side Conn over a fakeSocket, starts its
// reader and completes the 'M' / 'A'+id handshake. Without a handler the
// connection declares the "default" namespace with no events. The Conn is
// closed on test cleanup.
func newFakeClientConn(t *testing.T, handler ...ConnHandler) (*Conn, *fakeSocket) {
	t.Helper()

	s := newFakeSocket()
	return newFakeClientConnOn(t, s, s, handler...), s
}

// newFakeClientConnOn is newFakeClientConn over sock, which must read and
// write through s (s itself or fakeSocketBare{s}). It applies the handler's
// settings and starts the heartbeat after the ack, as Dial does.
func newFakeClientConnOn(t *testing.T, sock Socket, s *fakeSocket, handler ...ConnHandler) *Conn {
	t.Helper()

	var connHandler ConnHandler = Namespaces{fakeNamespace: Events{}}
	if len(handler) > 0 {
		connHandler = handler[0]
	}

	c := newConn(sock, connHandler.GetNamespaces())
	c.applySettings(getSettings(connHandler))
	t.Cleanup(c.Close)

	go c.startReader()

	// sendClientACK writes 'M' and waits for the remote 'A'+id, as Dial does.
	ackErr := make(chan error, 1)
	go func() { ackErr <- c.sendClientACK() }()

	if b := s.next(t); !bytes.Equal(b, ackBinaryB) {
		t.Fatalf("expected the client ack %q, got %q", ackBinaryB, b)
	}
	s.push(t, append(ackIDBinaryB, []byte("fake-client-id")...))

	select {
	case err := <-ackErr:
		if err != nil {
			t.Fatalf("client ack: %v", err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("timed out waiting for the client ack")
	}

	if c.ID() != "fake-client-id" {
		t.Fatalf("expected conn ID %q, got %q", "fake-client-id", c.ID())
	}

	c.startHeartbeat()
	return c
}

// connectFake connects the fake client Conn to namespace, answering the
// remote side's part of the handshake.
func connectFake(t *testing.T, c *Conn, s *fakeSocket, namespace string) *NSConn {
	t.Helper()

	type result struct {
		ns  *NSConn
		err error
	}
	done := make(chan result, 1)
	go func() {
		ns, err := c.Connect(context.Background(), namespace)
		done <- result{ns, err}
	}()

	s.answerNext(t)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("connect %q: %v", namespace, r.err)
		}
		return r.ns
	case <-time.After(fakeTimeout):
		t.Fatalf("connect %q did not return", namespace)
		return nil
	}
}

// joinFake joins ns to room, answering the remote side's part.
func joinFake(t *testing.T, ns *NSConn, s *fakeSocket, room string) *Room {
	t.Helper()

	type result struct {
		room *Room
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := ns.JoinRoom(context.Background(), room)
		done <- result{r, err}
	}()

	s.answerNext(t)

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("join %q: %v", room, r.err)
		}
		return r.room
	case <-time.After(fakeTimeout):
		t.Fatalf("join %q did not return", room)
		return nil
	}
}

// waitDone fails the test if ch is not closed or sent to within fakeTimeout.
func waitDone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(fakeTimeout):
		t.Fatalf("%s did not happen in time", what)
	}
}
