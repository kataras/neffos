package gobwas

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// peer is the raw gobwas side of a test connection.
type peer struct {
	conn  net.Conn
	state gobwas.State
	rd    *wsutil.Reader

	mu       sync.Mutex // serialises the peer's writes.
	autoPong atomic.Bool
	pings    atomic.Int32
}

func newPeer(t *testing.T, conn net.Conn, src io.Reader, state gobwas.State) *peer {
	t.Helper()
	t.Cleanup(func() { conn.Close() })

	return &peer{
		conn:  conn,
		state: state,
		rd:    &wsutil.Reader{Source: src, State: state},
	}
}

func (p *peer) write(op gobwas.OpCode, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return wsutil.WriteMessage(p.conn, p.state, op, payload)
}

func (p *peer) writeFrame(f gobwas.Frame) error {
	if p.state.ClientSide() {
		f = gobwas.MaskFrameInPlace(f)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return gobwas.WriteFrame(p.conn, f)
}

// read returns the next data message. Pongs are dropped, pings get a pong
// only when autoPong is set, and a close frame is answered and returned as
// a wsutil.ClosedError.
func (p *peer) read() (gobwas.OpCode, []byte, error) {
	for {
		hdr, err := p.rd.NextFrame()
		if err != nil {
			return 0, nil, err
		}

		if hdr.OpCode.IsControl() {
			if hdr.OpCode == gobwas.OpPing {
				p.pings.Add(1)
			}
			if hdr.OpCode == gobwas.OpPong || (hdr.OpCode == gobwas.OpPing && !p.autoPong.Load()) {
				if err := p.rd.Discard(); err != nil {
					return 0, nil, err
				}
				continue
			}

			p.mu.Lock()
			err = wsutil.ControlFrameHandler(p.conn, p.state)(hdr, p.rd)
			p.mu.Unlock()
			if err != nil {
				return 0, nil, err
			}
			continue
		}

		data, err := io.ReadAll(p.rd)
		return hdr.OpCode, data, err
	}
}

// readLoop keeps reading from the peer, which answers pings when autoPong is
// set.
func (p *peer) readLoop() {
	go func() {
		for {
			if _, _, err := p.read(); err != nil {
				return
			}
		}
	}()
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// serverPair upgrades one connection with the adapter and dials it with the
// raw gobwas client. The adapter is the server side.
func serverPair(t *testing.T) (*Socket, *peer) {
	t.Helper()

	socks := make(chan *Socket, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := DefaultUpgrader(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		socks <- s.(*Socket)
	}))
	t.Cleanup(srv.Close)

	conn, br, _, err := gobwas.Dial(context.Background(), wsURL(srv))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	var src io.Reader = conn
	if br != nil {
		src = br
	}
	p := newPeer(t, conn, src, gobwas.StateClientSide)

	select {
	case sock := <-socks:
		t.Cleanup(func() { sock.UnderlyingConn.Close() })
		return sock, p
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
		return nil, nil
	}
}

// clientPair dials a raw gobwas server with the adapter. The adapter is the
// client side.
func clientPair(t *testing.T) (*Socket, *peer) {
	t.Helper()

	conns := make(chan net.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := gobwas.UpgradeHTTP(r, w)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		conns <- conn
	}))
	t.Cleanup(srv.Close)

	s, err := DefaultDialer(context.Background(), wsURL(srv))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sock := s.(*Socket)
	t.Cleanup(func() { sock.UnderlyingConn.Close() })

	select {
	case conn := <-conns:
		return sock, newPeer(t, conn, conn, gobwas.StateServerSide)
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
		return nil, nil
	}
}

// bothSides runs fn with the adapter as the server side and as the client side.
func bothSides(t *testing.T, fn func(t *testing.T, sock *Socket, p *peer)) {
	t.Run("server", func(t *testing.T) {
		sock, p := serverPair(t)
		fn(t, sock, p)
	})
	t.Run("client", func(t *testing.T) {
		sock, p := clientPair(t)
		fn(t, sock, p)
	})
}

// readLoop reads from the adapter until it fails, so control frames get
// processed. It returns a channel with the final error.
func readLoop(sock *Socket, timeout time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() {
		for {
			if _, _, err := sock.ReadData(timeout); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}

// expectPeerClose reads from the peer until a close frame arrives and checks
// its code and, when wantReason is not nil, its reason.
func expectPeerClose(t *testing.T, p *peer, wantCode int, wantReason *string) {
	t.Helper()

	p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := p.read()
		if err == nil {
			continue
		}

		ce, ok := errors.AsType[wsutil.ClosedError](err)
		if !ok {
			t.Fatalf("expected a close frame, got %v", err)
		}
		if int(ce.Code) != wantCode {
			t.Fatalf("expected close code %d, got %d (%q)", wantCode, ce.Code, ce.Reason)
		}
		if wantReason != nil && ce.Reason != *wantReason {
			t.Fatalf("expected close reason %q, got %q", *wantReason, ce.Reason)
		}
		return
	}
}

func TestRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		if err := sock.WriteText([]byte("hello"), time.Second); err != nil {
			t.Fatal(err)
		}
		p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		op, data, err := p.read()
		if err != nil || op != gobwas.OpText || string(data) != "hello" {
			t.Fatalf("peer read: op %v, %q, %v", op, data, err)
		}

		if err := p.write(gobwas.OpBinary, []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
		data, typ, err := sock.ReadData(2 * time.Second)
		if err != nil || typ != neffos.BinaryMessage || !bytes.Equal(data, []byte{1, 2, 3}) {
			t.Fatalf("adapter read: type %v, %v, %v", typ, data, err)
		}
	})
}

func TestRemoteCloseMapsToCloseError(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		f := gobwas.NewCloseFrame(gobwas.NewCloseFrameBody(4001, "done"))
		if err := p.writeFrame(f); err != nil {
			t.Fatal(err)
		}

		_, _, err := sock.ReadData(2 * time.Second)
		ce, ok := errors.AsType[neffos.CloseError](err)
		if !ok {
			t.Fatalf("expected a neffos.CloseError, got %T: %v", err, err)
		}
		if ce.Code != 4001 || ce.Reason != "done" {
			t.Fatalf("expected [4001] done, got [%d] %q", ce.Code, ce.Reason)
		}
		if !neffos.IsCloseError(err) {
			t.Fatal("expected IsCloseError to be true")
		}

		// the adapter answers the close frame.
		p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := p.read(); !errors.As(err, new(wsutil.ClosedError)) {
			t.Fatalf("expected the close frame echoed back, got %v", err)
		}
	})
}

func TestCloseSendsStatus(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		if err := sock.Close(neffos.ClosePolicyViolation, "bye", time.Second); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reason := "bye"
		expectPeerClose(t, p, neffos.ClosePolicyViolation, &reason)

		if err := sock.WriteText([]byte("after"), time.Second); err == nil {
			t.Fatal("expected a write after Close to fail")
		}
	})
}

func TestReadLimit(t *testing.T) {
	over := func(t *testing.T, limit int64, send func(p *peer) error) {
		t.Helper()

		sock, p := serverPair(t)
		sock.SetReadLimit(limit)

		// a big message does not fit in the socket buffers, so write it
		// while the adapter reads. The write may fail once the adapter
		// closes.
		go send(p)

		_, _, err := sock.ReadData(2 * time.Second)
		if !errors.Is(err, neffos.ErrMessageTooBig) {
			t.Fatalf("expected ErrMessageTooBig, got %v", err)
		}
		expectPeerClose(t, p, neffos.CloseMessageTooBig, nil)
	}

	t.Run("frame over the limit", func(t *testing.T) {
		over(t, 64, func(p *peer) error { return p.write(gobwas.OpBinary, make([]byte, 1024)) })
	})

	t.Run("large frame over the limit", func(t *testing.T) {
		over(t, 64, func(p *peer) error { return p.write(gobwas.OpBinary, make([]byte, 256*1024)) })
	})

	t.Run("small frame over the limit", func(t *testing.T) {
		// 100 bytes is under the control frame floor of MaxFrameSize, so
		// the message length check catches it.
		over(t, 64, func(p *peer) error { return p.write(gobwas.OpBinary, make([]byte, 100)) })
	})

	t.Run("fragments over the limit", func(t *testing.T) {
		over(t, 200, func(p *peer) error {
			chunk := make([]byte, 100)
			frames := []gobwas.Frame{
				gobwas.NewFrame(gobwas.OpBinary, false, chunk),
				gobwas.NewFrame(gobwas.OpContinuation, false, chunk),
				gobwas.NewFrame(gobwas.OpContinuation, true, chunk),
			}
			for _, f := range frames {
				if err := p.writeFrame(f); err != nil {
					return err
				}
			}
			return nil
		})
	})

	t.Run("at the limit", func(t *testing.T) {
		sock, p := serverPair(t)
		sock.SetReadLimit(64)

		if err := p.write(gobwas.OpBinary, make([]byte, 64)); err != nil {
			t.Fatal(err)
		}
		data, _, err := sock.ReadData(2 * time.Second)
		if err != nil || len(data) != 64 {
			t.Fatalf("expected 64 bytes, got %d, %v", len(data), err)
		}
	})

	t.Run("removed", func(t *testing.T) {
		sock, p := serverPair(t)
		sock.SetReadLimit(64)
		sock.SetReadLimit(0)

		if err := p.write(gobwas.OpBinary, make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}
		data, _, err := sock.ReadData(2 * time.Second)
		if err != nil || len(data) != 1024 {
			t.Fatalf("expected 1024 bytes, got %d, %v", len(data), err)
		}
	})
}

func TestPingRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		p.autoPong.Store(true)
		p.readLoop()
		readLoop(sock, 0)

		for i := range 3 {
			if err := sock.Ping(time.Second); err != nil {
				t.Fatalf("Ping: %v", err)
			}
			if got := p.pings.Load(); got != int32(i+1) {
				t.Fatalf("expected the peer to see %d pings, saw %d", i+1, got)
			}
		}

		if err := sock.Ping(0); err != nil {
			t.Fatalf("Ping(0): %v", err)
		}
	})
}

func TestPingTimeout(t *testing.T) {
	sock, _ := serverPair(t) // the peer never reads, so no pong comes back.
	readLoop(sock, 0)

	start := time.Now()
	err := sock.Ping(150 * time.Millisecond)
	elapsed := time.Since(start)

	if !neffos.IsTimeoutError(err) {
		t.Fatalf("expected a timeout error, got %v", err)
	}
	if elapsed < 150*time.Millisecond || elapsed > time.Second {
		t.Fatalf("expected Ping to wait about 150ms, waited %s", elapsed)
	}
}

func TestPongExtendsReadDeadline(t *testing.T) {
	sock, p := serverPair(t)
	p.autoPong.Store(true)
	p.readLoop()

	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 1)
	go func() {
		data, _, err := sock.ReadData(300 * time.Millisecond)
		results <- result{data, err}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := sock.Ping(100 * time.Millisecond); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		select {
		case r := <-results:
			t.Fatalf("ReadData returned while pongs kept arriving: %q, %v", r.data, r.err)
		case <-time.After(100 * time.Millisecond):
		}
	}

	if err := p.write(gobwas.OpText, []byte("after")); err != nil {
		t.Fatal(err)
	}

	select {
	case r := <-results:
		if r.err != nil || string(r.data) != "after" {
			t.Fatalf("expected \"after\", got %q, %v", r.data, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadData did not return")
	}
}

// TestPongReplyNotInterleavedWithData floods the adapter with pings while
// four goroutines write 64 KB messages through it. Every pong reply must land
// between data frames, never inside one.
func TestPongReplyNotInterleavedWithData(t *testing.T) {
	sock, p := serverPair(t)
	readLoop(sock, 0)

	stop := make(chan struct{})
	var flood sync.WaitGroup
	flood.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := p.write(gobwas.OpPing, []byte("p")); err != nil {
				return
			}
		}
	})

	const writers, perWriter, size = 4, 16, 64 * 1024

	var wg sync.WaitGroup
	for g := range writers {
		wg.Go(func() {
			body := bytes.Repeat([]byte{'a' + byte(g)}, size)
			for range perWriter {
				if err := sock.WriteBinary(body, 5*time.Second); err != nil {
					t.Errorf("writer %d: %v", g, err)
					return
				}
			}
		})
	}

	counts := make(map[byte]int)
	p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for n := range writers * perWriter {
		op, data, err := p.read()
		if err != nil {
			t.Fatalf("peer read after %d messages: %v", n, err)
		}
		if op != gobwas.OpBinary || len(data) != size {
			t.Fatalf("message %d: op %v, %d bytes", n, op, len(data))
		}
		first := data[0]
		if first < 'a' || first >= 'a'+writers || bytes.IndexFunc(data, func(r rune) bool { return byte(r) != first }) != -1 {
			t.Fatalf("message %d is corrupted (starts with %q)", n, data[:8])
		}
		counts[first]++
	}

	close(stop)
	wg.Wait()
	flood.Wait()

	for g := range writers {
		if got := counts['a'+byte(g)]; got != perWriter {
			t.Errorf("writer %d: expected %d messages, got %d", g, perWriter, got)
		}
	}
}

// closingExchange closes the server while a connection is being set up, to
// reach the ErrServerClosed path of Server.Upgrade.
type closingExchange struct{ srv *neffos.Server }

func (e closingExchange) OnConnect(*neffos.Conn) error           { e.srv.Close(); return nil }
func (e closingExchange) OnDisconnect(*neffos.Conn)              {}
func (e closingExchange) Publish([]neffos.Message) bool          { return true }
func (e closingExchange) Subscribe(*neffos.Conn, string)         {}
func (e closingExchange) Unsubscribe(*neffos.Conn, string)       {}
func (e closingExchange) NotifyAsk(neffos.Message, string) error { return nil }
func (e closingExchange) Ask(context.Context, neffos.Message, string) (neffos.Message, error) {
	return neffos.Message{}, nil
}

// TestRejectedHandshakeCloseCodes checks the close code a raw client sees when
// the server refuses the connection.
func TestRejectedHandshakeCloseCodes(t *testing.T) {
	tests := []struct {
		name       string
		onConnect  error
		closing    bool
		wantCode   int
		wantReason string
	}{
		{name: "plain error", onConnect: errors.New("not allowed"), wantCode: neffos.ClosePolicyViolation, wantReason: "not allowed"},
		{name: "close error", onConnect: neffos.CloseError{Code: 4003, Reason: "custom"}, wantCode: 4003, wantReason: "custom"},
		{name: "server closed", closing: true, wantCode: neffos.CloseGoingAway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := neffos.New(DefaultUpgrader, neffos.Namespaces{"default": neffos.Events{}})
			if tt.closing {
				srv.StackExchange = closingExchange{srv}
			} else {
				srv.OnConnect = func(*neffos.Conn) error { return tt.onConnect }
			}
			hs := httptest.NewServer(srv)
			t.Cleanup(func() { srv.Close(); hs.Close() })

			conn, br, _, err := gobwas.Dial(context.Background(), wsURL(hs))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			var src io.Reader = conn
			if br != nil {
				src = br
			}
			p := newPeer(t, conn, src, gobwas.StateClientSide)

			// the neffos ack is a single 'M'. With a closing server the
			// connection is already gone, and on Windows a write that hits
			// the closed socket makes the next read fail with a reset before
			// the close frame is read, so skip it.
			if !tt.closing {
				if err := p.write(gobwas.OpBinary, []byte("M")); err != nil {
					t.Fatal(err)
				}
			}

			var reason *string
			if tt.wantReason != "" {
				reason = &tt.wantReason
			}
			expectPeerClose(t, p, tt.wantCode, reason)
		})
	}
}

// TestNoFramesAfterCloseFrame checks RFC 6455 5.5.1: once the socket has sent
// its close frame, nothing else goes on the wire.
func TestNoFramesAfterCloseFrame(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		body := gobwas.NewCloseFrameBody(neffos.ClosePolicyViolation, "bye")
		if err := sock.writeControl(gobwas.NewCloseFrame(body), time.Second); err != nil {
			t.Fatalf("close frame: %v", err)
		}

		if err := sock.WriteText([]byte("after"), time.Second); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("expected WriteText after the close frame to fail with net.ErrClosed, got %v", err)
		}
		if err := sock.WriteBinary([]byte("after"), time.Second); !neffos.IsCloseError(err) {
			t.Fatalf("expected IsCloseError for a write after the close frame, got %v", err)
		}

		start := time.Now()
		err := sock.Ping(time.Second)
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("expected Ping after the close frame to fail with net.ErrClosed, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
			t.Fatalf("expected Ping to fail at once, waited %s", elapsed)
		}

		// a second close frame is not written either.
		if err := sock.Close(neffos.CloseNormalClosure, "", time.Second); err != nil {
			t.Fatalf("Close: %v", err)
		}

		// the peer sees the close frame and then nothing.
		p.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		reason := "bye"
		expectPeerClose(t, p, neffos.ClosePolicyViolation, &reason)

		p.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if _, data, err := p.read(); err == nil {
			t.Fatalf("expected no frame after the close frame, got %q", data)
		}
	})
}

// TestHardDropMapsToAbnormalClosure pins the contract both backends share: a
// connection that dies without a close frame reports CloseAbnormalClosure.
func TestHardDropMapsToAbnormalClosure(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		// close the TCP connection with no close frame, so the adapter
		// reads EOF. A reset (SetLinger(0)) is a different case: it surfaces
		// as a syscall error with no close status on either backend.
		p.conn.Close()

		_, _, err := sock.ReadData(2 * time.Second)
		if got := neffos.CloseStatus(err); got != neffos.CloseAbnormalClosure {
			t.Fatalf("expected close status %d, got %d (%v)", neffos.CloseAbnormalClosure, got, err)
		}
		if !neffos.IsCloseError(err) {
			t.Fatalf("expected IsCloseError to be true, got %v", err)
		}
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected the original io.ErrUnexpectedEOF in the chain, got %v", err)
		}
	})
}

// TestFrameRightAfterHandshakeNotLost sends the upgrade request and a first
// frame in one TCP write, so net/http has read the frame into its buffer
// before the handshake ends. The adapter must still read it.
func TestFrameRightAfterHandshakeNotLost(t *testing.T) {
	socks := make(chan *Socket, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := DefaultUpgrader(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		socks <- s.(*Socket)
	}))
	t.Cleanup(srv.Close)

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	var buf bytes.Buffer
	buf.WriteString("GET / HTTP/1.1\r\n" +
		"Host: " + srv.Listener.Addr().String() + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n")
	frame := gobwas.MaskFrameInPlace(gobwas.NewTextFrame([]byte("first")))
	if err := gobwas.WriteFrame(&buf, frame); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}

	var sock *Socket
	select {
	case sock = <-socks:
		t.Cleanup(func() { sock.UnderlyingConn.Close() })
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
	}

	data, typ, err := sock.ReadData(time.Second)
	if err != nil || typ != neffos.TextMessage || string(data) != "first" {
		t.Fatalf("expected the text frame \"first\", got %v %q, %v", typ, data, err)
	}
}

// TestClientFrameRightAfterHandshakeNotLost has a raw server write the 101
// response and a first frame in one TCP write, so the dialer reads the frame
// into its handshake buffer. The adapter must still read it.
func TestClientFrameRightAfterHandshakeNotLost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		t.Cleanup(func() { conn.Close() })

		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		var buf bytes.Buffer
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		if err := gobwas.WriteFrame(&buf, gobwas.NewTextFrame([]byte("first"))); err != nil {
			t.Errorf("frame: %v", err)
			return
		}
		conn.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)

	s, err := DefaultDialer(context.Background(), wsURL(srv))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sock := s.(*Socket)
	t.Cleanup(func() { sock.UnderlyingConn.Close() })

	data, typ, err := sock.ReadData(time.Second)
	if err != nil || typ != neffos.TextMessage || string(data) != "first" {
		t.Fatalf("expected the text frame \"first\", got %v %q, %v", typ, data, err)
	}
}
