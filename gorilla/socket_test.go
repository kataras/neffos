package gorilla

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kataras/neffos"

	gorilla "github.com/gorilla/websocket"
)

// serverPair upgrades one connection with the adapter and dials it with the
// raw gorilla client. The adapter is the server side.
func serverPair(t *testing.T) (*Socket, *gorilla.Conn) {
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

	peer, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { peer.Close() })

	select {
	case sock := <-socks:
		t.Cleanup(func() { sock.UnderlyingConn.Close() })
		return sock, peer
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
		return nil, nil
	}
}

// clientPair dials a raw gorilla server with the adapter. The adapter is the
// client side.
func clientPair(t *testing.T) (*Socket, *gorilla.Conn) {
	t.Helper()

	peers := make(chan *gorilla.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var up gorilla.Upgrader
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		peers <- c
	}))
	t.Cleanup(srv.Close)

	s, err := DefaultDialer(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	sock := s.(*Socket)
	t.Cleanup(func() { sock.UnderlyingConn.Close() })

	select {
	case peer := <-peers:
		t.Cleanup(func() { peer.Close() })
		return sock, peer
	case <-time.After(2 * time.Second):
		t.Fatal("upgrade did not happen")
		return nil, nil
	}
}

// bothSides runs fn with the adapter as the server side and as the client side.
func bothSides(t *testing.T, fn func(t *testing.T, sock *Socket, peer *gorilla.Conn)) {
	t.Run("server", func(t *testing.T) {
		sock, peer := serverPair(t)
		fn(t, sock, peer)
	})
	t.Run("client", func(t *testing.T) {
		sock, peer := clientPair(t)
		fn(t, sock, peer)
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

// peerReadLoop reads from the raw peer until it fails, which makes gorilla
// answer pings with pongs.
func peerReadLoop(peer *gorilla.Conn) {
	go func() {
		for {
			if _, _, err := peer.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

// expectPeerClose reads from the raw peer until a close frame arrives and
// checks its code and, when wantReason is not nil, its text.
func expectPeerClose(t *testing.T, peer *gorilla.Conn, wantCode int, wantReason *string) {
	t.Helper()

	peer.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, _, err := peer.ReadMessage()
		if err == nil {
			continue
		}

		var ce *gorilla.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("expected a close frame, got %v", err)
		}
		if ce.Code != wantCode {
			t.Fatalf("expected close code %d, got %d (%q)", wantCode, ce.Code, ce.Text)
		}
		if wantReason != nil && ce.Text != *wantReason {
			t.Fatalf("expected close reason %q, got %q", *wantReason, ce.Text)
		}
		return
	}
}

func TestRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, peer *gorilla.Conn) {
		if err := sock.WriteText([]byte("hello"), time.Second); err != nil {
			t.Fatal(err)
		}
		peer.SetReadDeadline(time.Now().Add(2 * time.Second))
		typ, data, err := peer.ReadMessage()
		if err != nil || typ != gorilla.TextMessage || string(data) != "hello" {
			t.Fatalf("peer read: type %d, %q, %v", typ, data, err)
		}

		if err := peer.WriteMessage(gorilla.BinaryMessage, []byte{1, 2, 3}); err != nil {
			t.Fatal(err)
		}
		data, typ2, err := sock.ReadData(2 * time.Second)
		if err != nil || typ2 != neffos.BinaryMessage || !bytes.Equal(data, []byte{1, 2, 3}) {
			t.Fatalf("adapter read: type %v, %v, %v", typ2, data, err)
		}
	})
}

func TestRemoteCloseMapsToCloseError(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, peer *gorilla.Conn) {
		msg := gorilla.FormatCloseMessage(4001, "done")
		if err := peer.WriteControl(gorilla.CloseMessage, msg, time.Now().Add(time.Second)); err != nil {
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
	})
}

func TestCloseSendsStatus(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, peer *gorilla.Conn) {
		if err := sock.Close(neffos.ClosePolicyViolation, "bye", time.Second); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reason := "bye"
		expectPeerClose(t, peer, neffos.ClosePolicyViolation, &reason)

		if err := sock.WriteText([]byte("after"), time.Second); err == nil {
			t.Fatal("expected a write after Close to fail")
		}
	})
}

func TestReadLimit(t *testing.T) {
	for _, size := range []int{1024, 256 * 1024} {
		t.Run(fmt.Sprintf("over by %d", size), func(t *testing.T) {
			sock, peer := serverPair(t)
			sock.SetReadLimit(64)

			// a big message does not fit in the socket buffers, so write it
			// while the adapter reads. The write may fail once the adapter
			// closes.
			go peer.WriteMessage(gorilla.BinaryMessage, make([]byte, size))

			_, _, err := sock.ReadData(2 * time.Second)
			if !errors.Is(err, neffos.ErrMessageTooBig) {
				t.Fatalf("expected ErrMessageTooBig, got %v", err)
			}
			expectPeerClose(t, peer, neffos.CloseMessageTooBig, nil)
		})
	}

	t.Run("removed", func(t *testing.T) {
		sock, peer := serverPair(t)
		sock.SetReadLimit(64)
		sock.SetReadLimit(0)

		if err := peer.WriteMessage(gorilla.BinaryMessage, make([]byte, 1024)); err != nil {
			t.Fatal(err)
		}

		data, _, err := sock.ReadData(2 * time.Second)
		if err != nil || len(data) != 1024 {
			t.Fatalf("expected 1024 bytes, got %d, %v", len(data), err)
		}
	})
}

func TestPingRoundTrip(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, peer *gorilla.Conn) {
		pongs := make(chan string, 1)
		peer.SetPingHandler(func(data string) error {
			pongs <- data
			return peer.WriteControl(gorilla.PongMessage, []byte(data), time.Now().Add(time.Second))
		})
		peerReadLoop(peer)
		readLoop(sock, 0)

		for range 3 {
			if err := sock.Ping(time.Second); err != nil {
				t.Fatalf("Ping: %v", err)
			}
			select {
			case <-pongs:
			default:
				t.Fatal("Ping returned but the peer saw no ping")
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
	sock, peer := serverPair(t)
	peerReadLoop(peer)

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

	if err := peer.WriteMessage(gorilla.TextMessage, []byte("after")); err != nil {
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

func TestWriteDeadlineUnderLock(t *testing.T) {
	sock, peer := serverPair(t)

	const writers, perWriter = 8, 50

	var wg sync.WaitGroup
	for g := range writers {
		wg.Go(func() {
			for i := range perWriter {
				if err := sock.WriteText(fmt.Appendf(nil, "msg-%d-%d", g, i), time.Second); err != nil {
					t.Errorf("write %d-%d: %v", g, i, err)
					return
				}
			}
		})
	}

	seen := make(map[string]bool, writers*perWriter)
	peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	for range writers * perWriter {
		typ, data, err := peer.ReadMessage()
		if err != nil {
			t.Fatalf("peer read after %d messages: %v", len(seen), err)
		}
		var g, i int
		if typ != gorilla.TextMessage || !strings.HasPrefix(string(data), "msg-") {
			t.Fatalf("unexpected frame: type %d, %q", typ, data)
		}
		if _, err := fmt.Sscanf(string(data), "msg-%d-%d", &g, &i); err != nil {
			t.Fatalf("unexpected frame %q: %v", data, err)
		}
		if seen[string(data)] {
			t.Fatalf("duplicate frame %q", data)
		}
		seen[string(data)] = true
	}

	wg.Wait()
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

			peer, _, err := gorilla.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http"), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			t.Cleanup(func() { peer.Close() })

			// the neffos ack is a single 'M'. With a closing server the
			// connection is already gone, and on Windows a write that hits
			// the closed socket makes the next read fail with a reset before
			// the close frame is read, so skip it.
			if !tt.closing {
				if err := peer.WriteMessage(gorilla.BinaryMessage, []byte("M")); err != nil {
					t.Fatal(err)
				}
			}

			var reason *string
			if tt.wantReason != "" {
				reason = &tt.wantReason
			}
			expectPeerClose(t, peer, tt.wantCode, reason)
		})
	}
}

// TestHardDropMapsToAbnormalClosure pins the contract both backends share: a
// connection that dies without a close frame reports CloseAbnormalClosure.
func TestHardDropMapsToAbnormalClosure(t *testing.T) {
	bothSides(t, func(t *testing.T, sock *Socket, peer *gorilla.Conn) {
		// close the TCP connection with no close frame, so the adapter
		// reads EOF. A reset (SetLinger(0)) is a different case: it surfaces
		// as a syscall error with no close status on either backend.
		peer.NetConn().Close()

		_, _, err := sock.ReadData(2 * time.Second)
		if got := neffos.CloseStatus(err); got != neffos.CloseAbnormalClosure {
			t.Fatalf("expected close status %d, got %d (%v)", neffos.CloseAbnormalClosure, got, err)
		}
		if !neffos.IsCloseError(err) {
			t.Fatalf("expected IsCloseError to be true, got %v", err)
		}
	})
}
