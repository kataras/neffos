package coder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/neffos"

	"github.com/coder/websocket"
)

// peer is the raw coder side of a test connection. It counts the pings it
// receives; coder answers them with pongs while the peer is inside Read.
type peer struct {
	conn  *websocket.Conn
	pings atomic.Int32
}

func (p *peer) onPing(context.Context, []byte) bool {
	p.pings.Add(1)
	return true
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// serverPair upgrades one connection with the adapter and dials it with the
// raw coder client. The adapter is the server side.
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

	p := new(peer)
	conn, _, err := websocket.Dial(context.Background(), wsURL(srv), &websocket.DialOptions{OnPingReceived: p.onPing})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.SetReadLimit(-1)
	p.conn = conn
	t.Cleanup(func() { conn.CloseNow() })

	select {
	case sock := <-socks:
		t.Cleanup(func() { sock.UnderlyingConn.CloseNow() })
		return sock, p
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
		return nil, nil
	}
}

// clientPair dials a raw coder server with the adapter. The adapter is the
// client side.
func clientPair(t *testing.T) (*Socket, *peer) {
	t.Helper()

	p := new(peer)
	conns := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OnPingReceived: p.onPing})
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		c.SetReadLimit(-1)
		conns <- c
	}))
	t.Cleanup(srv.Close)

	s, err := DefaultDialer(context.Background(), wsURL(srv))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sock := s.(*Socket)
	t.Cleanup(func() { sock.UnderlyingConn.CloseNow() })

	select {
	case conn := <-conns:
		p.conn = conn
		t.Cleanup(func() { conn.CloseNow() })
		return sock, p
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

// peerReadLoop reads from the raw peer until it fails and returns a channel
// with the final error. While it runs, coder answers pings and close frames.
func peerReadLoop(p *peer) <-chan error {
	done := make(chan error, 1)
	go func() {
		for {
			if _, _, err := p.conn.Read(context.Background()); err != nil {
				done <- err
				return
			}
		}
	}()
	return done
}

// checkClose checks that err carries a close frame with wantCode and, when
// wantReason is not nil, its reason.
func checkClose(t *testing.T, err error, wantCode int, wantReason *string) {
	t.Helper()

	var ce websocket.CloseError
	if !errors.As(err, &ce) {
		t.Fatalf("expected a close frame, got %v", err)
	}
	if int(ce.Code) != wantCode {
		t.Fatalf("expected close code %d, got %d (%q)", wantCode, ce.Code, ce.Reason)
	}
	if wantReason != nil && ce.Reason != *wantReason {
		t.Fatalf("expected close reason %q, got %q", *wantReason, ce.Reason)
	}
}

// waitErr waits up to two seconds for the error a read loop ended with.
func waitErr(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("the read loop did not end")
		return nil
	}
}

func TestRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		if err := sock.WriteText([]byte("hello"), time.Second); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		typ, data, err := p.conn.Read(ctx)
		if err != nil || typ != websocket.MessageText || string(data) != "hello" {
			t.Fatalf("peer read: type %v, %q, %v", typ, data, err)
		}

		if err := p.conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
		data, typ2, err := sock.ReadData(2 * time.Second)
		if err != nil || typ2 != neffos.BinaryMessage || !bytes.Equal(data, []byte{1, 2, 3}) {
			t.Fatalf("adapter read: type %v, %v, %v", typ2, data, err)
		}
	})
}

func TestRemoteCloseMapsToCloseError(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		// the raw Close waits for our reply, which ReadData sends.
		go p.conn.Close(4001, "done")

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
	})
}

func TestCloseSendsStatus(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		peerDone := peerReadLoop(p)

		start := time.Now()
		if err := sock.Close(neffos.ClosePolicyViolation, "bye", time.Second); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("expected Close to finish with the peer's reply, took %s", elapsed)
		}

		reason := "bye"
		checkClose(t, waitErr(t, peerDone), neffos.ClosePolicyViolation, &reason)

		if err := sock.WriteText([]byte("after"), time.Second); err == nil {
			t.Fatal("expected a write after Close to fail")
		}
	})
}

func TestReadLimit(t *testing.T) {
	for _, size := range []int{1024, 256 * 1024} {
		t.Run(fmt.Sprintf("over by %d", size), func(t *testing.T) {
			sock, p := serverPair(t)
			sock.SetReadLimit(64)
			peerDone := peerReadLoop(p)

			// a big message does not fit in the socket buffers, so write it
			// while the adapter reads. The write may fail once the adapter
			// closes.
			go p.conn.Write(context.Background(), websocket.MessageBinary, make([]byte, size))

			_, _, err := sock.ReadData(2 * time.Second)
			if !errors.Is(err, neffos.ErrMessageTooBig) {
				t.Fatalf("expected ErrMessageTooBig, got %v", err)
			}
			checkClose(t, waitErr(t, peerDone), neffos.CloseMessageTooBig, nil)
		})
	}

	t.Run("kept after NetConn", func(t *testing.T) {
		sock, p := serverPair(t)
		sock.SetReadLimit(64)
		_ = sock.NetConn() // coder's NetConn turns the limit off.
		peerDone := peerReadLoop(p)

		go p.conn.Write(context.Background(), websocket.MessageBinary, make([]byte, 1024))

		_, _, err := sock.ReadData(2 * time.Second)
		if !errors.Is(err, neffos.ErrMessageTooBig) {
			t.Fatalf("expected ErrMessageTooBig, got %v", err)
		}
		checkClose(t, waitErr(t, peerDone), neffos.CloseMessageTooBig, nil)
	})

	t.Run("no limit by default", func(t *testing.T) {
		sock, p := serverPair(t)

		// coder's own default is 32 KB.
		const size = 64 * 1024
		go p.conn.Write(context.Background(), websocket.MessageBinary, make([]byte, size))

		data, _, err := sock.ReadData(2 * time.Second)
		if err != nil || len(data) != size {
			t.Fatalf("expected %d bytes, got %d, %v", size, len(data), err)
		}
	})

	t.Run("removed", func(t *testing.T) {
		sock, p := serverPair(t)
		sock.SetReadLimit(64)
		sock.SetReadLimit(0)

		go p.conn.Write(context.Background(), websocket.MessageBinary, make([]byte, 1024))

		data, _, err := sock.ReadData(2 * time.Second)
		if err != nil || len(data) != 1024 {
			t.Fatalf("expected 1024 bytes, got %d, %v", len(data), err)
		}
	})
}

func TestPingRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		peerReadLoop(p)
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
	peerReadLoop(p)

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

	if err := p.conn.Write(context.Background(), websocket.MessageText, []byte("after")); err != nil {
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

// TestReadTimeout checks that a read with no message in time reports a
// timeout and that the connection is closed after it, as coder does.
func TestReadTimeout(t *testing.T) {
	sock, _ := serverPair(t)

	start := time.Now()
	_, _, err := sock.ReadData(150 * time.Millisecond)
	elapsed := time.Since(start)

	if !neffos.IsTimeoutError(err) {
		t.Fatalf("expected a timeout error, got %v", err)
	}
	if elapsed < 150*time.Millisecond || elapsed > time.Second {
		t.Fatalf("expected ReadData to wait about 150ms, waited %s", elapsed)
	}
	if err := sock.WriteText([]byte("after"), time.Second); err == nil {
		t.Fatal("expected a write after the read timeout to fail")
	}
}

// TestNetConnCloseDoesNotHandshake checks that closing NetConn() closes at
// once. coder's own net.Conn runs the close handshake, which waits up to 5s
// for a peer that never answers.
func TestNetConnCloseDoesNotHandshake(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		start := time.Now()
		sock.NetConn().Close()
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("expected Close to return at once, took %s", elapsed)
		}

		if err := sock.WriteText([]byte("after"), time.Second); err == nil {
			t.Fatal("expected a write after Close to fail")
		}
	})
}

// TestCloseBoundedByTimeout checks that Close against a peer that never
// answers returns after its timeout, with a ReadData in flight as neffos
// always has one.
func TestCloseBoundedByTimeout(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		readDone := readLoop(sock, 0)

		start := time.Now()
		sock.Close(neffos.CloseNormalClosure, "", 200*time.Millisecond)
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("expected Close to return after about 200ms, took %s", elapsed)
		}

		if err := waitErr(t, readDone); err == nil {
			t.Fatal("expected the read to fail after Close")
		}
	})
}

// TestOriginPatternsPassthrough checks that the AcceptOptions reach coder:
// a cross-origin request fails by default and passes with a matching pattern.
func TestOriginPatternsPassthrough(t *testing.T) {
	dial := func(t *testing.T, up neffos.Upgrader) (*http.Response, error) {
		t.Helper()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, err := up(w, r)
			if err == nil {
				s.(*Socket).UnderlyingConn.CloseNow()
			}
		}))
		t.Cleanup(srv.Close)

		header := http.Header{"Origin": {"http://other.example"}}
		conn, resp, err := websocket.Dial(context.Background(), wsURL(srv), &websocket.DialOptions{HTTPHeader: header})
		if err == nil {
			conn.CloseNow()
		}
		return resp, err
	}

	t.Run("default", func(t *testing.T) {
		resp, err := dial(t, DefaultUpgrader)
		if err == nil {
			t.Fatal("expected a cross-origin request to fail")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("expected the origin check to answer 403, got %v (%v)", resp, err)
		}
	})

	t.Run("pattern", func(t *testing.T) {
		up := Upgrader(websocket.AcceptOptions{OriginPatterns: []string{"other.example"}})
		if _, err := dial(t, up); err != nil {
			t.Fatalf("expected the cross-origin request to pass, got %v", err)
		}
	})
}

// TestUserPongCallbackKept checks that a pong callback in the options still
// runs next to the socket's own pong handling.
func TestUserPongCallbackKept(t *testing.T) {
	var pongs atomic.Int32
	socks := make(chan *Socket, 1)
	up := Upgrader(websocket.AcceptOptions{OnPongReceived: func(context.Context, []byte) { pongs.Add(1) }})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := up(w, r)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		socks <- s.(*Socket)
	}))
	t.Cleanup(srv.Close)

	conn, _, err := websocket.Dial(context.Background(), wsURL(srv), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	peerReadLoop(&peer{conn: conn})

	sock := <-socks
	t.Cleanup(func() { sock.UnderlyingConn.CloseNow() })
	readLoop(sock, 0)

	if err := sock.Ping(time.Second); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got := pongs.Load(); got != 1 {
		t.Fatalf("expected the user callback to see 1 pong, saw %d", got)
	}
}

// TestHardDropMapsToAbnormalClosure pins the contract every backend shares: a
// connection that dies without a close frame reports CloseAbnormalClosure.
func TestHardDropMapsToAbnormalClosure(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, p *peer) {
		// CloseNow closes the transport with no close frame.
		p.conn.CloseNow()

		_, _, err := sock.ReadData(2 * time.Second)
		if got := neffos.CloseStatus(err); got != neffos.CloseAbnormalClosure {
			t.Fatalf("expected close status %d, got %d (%v)", neffos.CloseAbnormalClosure, got, err)
		}
		if !neffos.IsCloseError(err) {
			t.Fatalf("expected IsCloseError to be true, got %v", err)
		}
	})
}
