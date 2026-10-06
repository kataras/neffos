package neffos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/neffos"

	"github.com/kataras/neffos/coder"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"

	"golang.org/x/sync/errgroup"
)

// upgraders and dialers are keyed by backend name, so every integration test
// runs on each backend.
var upgraders = map[string]neffos.Upgrader{
	"gorilla": gorilla.DefaultUpgrader,
	"gobwas":  gobwas.DefaultUpgrader,
	"coder":   coder.DefaultUpgrader,
}

var dialers = map[string]neffos.Dialer{
	"gorilla": gorilla.DefaultDialer,
	"gobwas":  gobwas.DefaultDialer,
	"coder":   coder.DefaultDialer,
}

// testServers serves one neffos.Server per backend behind a single
// httptest.Server bound to 127.0.0.1:0, each under its own "/<backend>" route.
type testServers struct {
	addr       string // host:port, no scheme.
	servers    map[string]*neffos.Server
	httpServer *httptest.Server
}

// newTestServers starts a neffos.Server per entry in `upgraders`, all served
// by one httptest.Server, and registers a t.Cleanup that closes the neffos
// servers and then the http server.
func newTestServers(t *testing.T, handler neffos.ConnHandler, cfg ...func(*neffos.Server)) *testServers {
	t.Helper()

	servers := make(map[string]*neffos.Server, len(upgraders))
	mux := http.NewServeMux()

	for backend, upgrader := range upgraders {
		srv := neffos.New(upgrader, handler)
		for _, c := range cfg {
			c(srv)
		}

		servers[backend] = srv
		mux.Handle("/"+backend, srv)
	}

	httpServer := httptest.NewServer(mux)

	t.Cleanup(func() {
		for _, srv := range servers {
			srv.Close()
		}
		httpServer.Close()
	})

	return &testServers{
		addr:       strings.TrimPrefix(httpServer.URL, "http://"),
		servers:    servers,
		httpServer: httpServer,
	}
}

func TestServerBroadcastTo(t *testing.T) {
	// we fire up two connections, one with the "conn_ID" and other with the default uuid id generator,
	// the message which the second client emits should only be sent to the connection with the ID of "conn_ID".

	var (
		wg        sync.WaitGroup
		namespace = "default"
		body      = []byte("data")
		to        = "conn_ID"
		events    = neffos.Namespaces{
			namespace: neffos.Events{
				"event": func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						defer wg.Done()

						if !bytes.Equal(msg.Body, body) {
							t.Errorf("expected event's incoming data to be: %s but got: %s", string(body), string(msg.Body))
						}

						if c.String() != to {
							t.Errorf("expected the message to be sent only to the connection with an ID of 'conn_ID'")
						}
					} else {
						msg.To = to
						c.Conn.Server().Broadcast(c, msg)
					}

					return nil
				},
			},
		}
	)

	ts := newTestServers(t, events, func(wsServer *neffos.Server) {
		once := new(uint32)
		wsServer.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
			if atomic.CompareAndSwapUint32(once, 0, 1) {
				return to // set the "to" only to the first conn for test.
			}

			return neffos.DefaultIDGenerator(w, r)
		}
	})

	wg.Add(len(upgraders)) // one per backend.

	ts.dial(t, events, func(backend string, client *neffos.Client) {
		_, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}
	})

	ts.dial(t, events, func(backend string, client *neffos.Client) {
		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}
		c.Emit("event", body)
	})

	wg.Wait()
}

func TestServerAsk(t *testing.T) {
	// we fire up two connections, one with the "conn_ID" and other with the default uuid id generator,
	// the message which the second client emits should only be sent to the connection with the ID of "conn_ID".

	var (
		wg             sync.WaitGroup
		namespace      = "default"
		body           = []byte("data")
		expectResponse = append(body, []byte("ok")...)
		to             = "conn_ID"
		clientEvents   = neffos.Namespaces{
			namespace: neffos.Events{
				"ask": func(c *neffos.NSConn, msg neffos.Message) error {
					return neffos.Reply(expectResponse)
				},
			},
		}
	)

	g := new(errgroup.Group)

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{}}, func(wsServer *neffos.Server) {
		once := new(uint32)
		wsServer.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
			if atomic.CompareAndSwapUint32(once, 0, 1) {
				return to // set the "to" only to the first conn for test.
			}

			return neffos.DefaultIDGenerator(w, r)
		}

		wgWaitToAllConnect := new(sync.WaitGroup)
		wgWaitToAllConnect.Add(2)
		wsServer.OnConnect = func(c *neffos.Conn) error {
			wgWaitToAllConnect.Done()
			return nil
		}

		worker := func() error {
			wgWaitToAllConnect.Wait()

			response, err := wsServer.Ask(context.TODO(), neffos.Message{
				Namespace: "default",
				Event:     "ask",
				To:        to,
			})

			if err != nil {
				return err
			}

			if !bytes.Equal(response.Body, expectResponse) {
				return fmt.Errorf("expected response with body: %s but got: %s", string(expectResponse), string(response.Body))
			}

			wg.Done()
			return nil
		}

		g.Go(worker)
	})

	wg.Add(len(upgraders)) // one per backend.

	ts.dial(t, clientEvents, func(backend string, client *neffos.Client) {
		_, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}
	})

	ts.dial(t, clientEvents, func(backend string, client *neffos.Client) {
		_, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}
	})

	wg.Wait()
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}

// within fails the test if fn does not return within 2 seconds.
func within(t *testing.T, what string, fn func()) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not return within 2s", what)
	}
}

func TestServerCloseUnblocksBroadcastDoUpgrade(t *testing.T) {
	ts := newTestServers(t, neffos.Namespaces{"default": neffos.Events{}}, func(srv *neffos.Server) {
		srv.SyncBroadcaster = true
	})

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(string, *neffos.Client) {})

	for backend, srv := range ts.servers {
		within(t, backend+": Close", srv.Close)

		within(t, backend+": Broadcast", func() {
			srv.Broadcast(nil, neffos.Message{Namespace: "default", Event: "event"})
		})

		within(t, backend+": Do", func() {
			srv.Do(func(*neffos.Conn) {
				t.Errorf("%s: Do called fn after Close", backend)
			}, false)
		})

		within(t, backend+": Ask", func() {
			_, err := srv.Ask(context.Background(), neffos.Message{Namespace: "default", Event: "event"})
			if !errors.Is(err, neffos.ErrServerClosed) {
				t.Errorf("%s: expected Ask to return ErrServerClosed, got %v", backend, err)
			}
		})

		within(t, backend+": Upgrade", func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/"+backend, nil)
			if _, err := srv.Upgrade(rec, req, nil, nil); !errors.Is(err, neffos.ErrServerClosed) {
				t.Errorf("%s: expected Upgrade to return ErrServerClosed, got %v", backend, err)
			}
		})
	}
}

func TestServerCloseFiresOnDisconnectForEveryConn(t *testing.T) {
	const clients = 5

	var (
		mu    sync.Mutex
		fired = make(map[string]map[string]int) // backend -> conn ID -> OnDisconnect count.
	)

	ts := newTestServers(t, neffos.Namespaces{"default": neffos.Events{}})
	for backend, srv := range ts.servers {
		fired[backend] = make(map[string]int)
		srv.OnDisconnect = func(c *neffos.Conn) {
			mu.Lock()
			fired[backend][c.ID()]++
			mu.Unlock()
		}
	}

	for range clients {
		ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(string, *neffos.Client) {})
	}

	for backend, srv := range ts.servers {
		if got := srv.GetTotalConnections(); got != clients {
			t.Fatalf("%s: expected %d connections before Close, got %d", backend, clients, got)
		}

		srv.Close()

		mu.Lock()
		got := fired[backend]
		if len(got) != clients {
			t.Errorf("%s: expected OnDisconnect for %d connections when Close returns, got %d", backend, clients, len(got))
		}
		for id, n := range got {
			if n != 1 {
				t.Errorf("%s: OnDisconnect fired %d times for %s", backend, n, id)
			}
		}
		mu.Unlock()

		if n := srv.GetTotalConnections(); n != 0 {
			t.Errorf("%s: expected 0 connections after Close, got %d", backend, n)
		}
	}
}

func TestGetConnectionsDuringUpgrades(t *testing.T) {
	const clients = 10

	events := neffos.Namespaces{"default": neffos.Events{}}
	ts := newTestServers(t, events)

	for backend, srv := range ts.servers {
		stop := make(chan struct{})
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for {
				select {
				case <-stop:
					return
				default:
				}

				_ = srv.GetConnections()
				_ = srv.GetConnectionsByNamespace("default")
				// a Do callback may read the connection set too.
				srv.Do(func(*neffos.Conn) { _ = srv.GetConnections() }, false)
			}
		}()

		dialer := dialers[backend]
		url := fmt.Sprintf("ws://%s/%s", ts.addr, backend)

		errs := make(chan error, clients)
		var wg sync.WaitGroup
		for range clients {
			wg.Add(1)
			go func() {
				defer wg.Done()
				client, err := neffos.Dial(context.Background(), dialer, url, events)
				if err != nil {
					errs <- err
					return
				}
				t.Cleanup(client.Close)
			}()
		}

		within(t, backend+": dials", wg.Wait)
		close(stop)
		within(t, backend+": GetConnections loop", func() { <-readerDone })

		close(errs)
		for err := range errs {
			t.Fatalf("%s: dial: %v", backend, err)
		}

		if got := len(srv.GetConnections()); got != clients {
			t.Fatalf("%s: expected %d connections, got %d", backend, clients, got)
		}
		if got := srv.GetTotalConnections(); got != clients {
			t.Fatalf("%s: expected a total of %d connections, got %d", backend, clients, got)
		}
	}
}

func TestUpgradeAfterCloseReturnsErrServerClosed(t *testing.T) {
	t.Run("closed before the request", func(t *testing.T) {
		srv := neffos.New(gorilla.DefaultUpgrader, neffos.Namespaces{"default": neffos.Events{}})
		srv.Close()

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		c, err := srv.Upgrade(rec, req, nil, nil)
		if !errors.Is(err, neffos.ErrServerClosed) {
			t.Fatalf("expected ErrServerClosed, got %v", err)
		}
		if c != nil {
			t.Fatal("expected a nil connection")
		}
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
		}
	})

	// Close lands after the early closed check but before registration: the
	// upgrader itself closes the server. The connection must not be
	// registered or returned.
	t.Run("closed during the upgrade", func(t *testing.T) {
		var srv *neffos.Server
		upgrader := func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
			socket, err := gorilla.DefaultUpgrader(w, r)
			srv.Close()
			return socket, err
		}
		srv = neffos.New(upgrader, neffos.Namespaces{"default": neffos.Events{}})

		type result struct {
			c   *neffos.Conn
			err error
		}
		results := make(chan result, 1)
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := srv.Upgrade(w, r, nil, nil)
			results <- result{c, err}
		}))
		t.Cleanup(httpServer.Close)

		url := "ws://" + strings.TrimPrefix(httpServer.URL, "http://")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if client, err := neffos.Dial(ctx, gorilla.DefaultDialer, url, neffos.Namespaces{"default": neffos.Events{}}); err == nil {
			client.Close()
			t.Error("expected Dial to fail against a closed server")
		}

		select {
		case r := <-results:
			if !errors.Is(r.err, neffos.ErrServerClosed) {
				t.Fatalf("expected ErrServerClosed, got %v", r.err)
			}
			if r.c != nil {
				t.Fatal("expected a nil connection")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Upgrade did not return")
		}

		if n := len(srv.GetConnections()); n != 0 {
			t.Fatalf("expected no registered connections, got %d", n)
		}
	})
}

// blockingServer serves a "block" event whose callback signals entered and
// then waits until the test closes the server's release channel.
func blockingServer(t *testing.T) (ts *testServers, entered chan string, releases map[*neffos.Server]chan struct{}) {
	t.Helper()

	entered = make(chan string, len(upgraders))
	releases = make(map[*neffos.Server]chan struct{})
	ts = newTestServers(t, neffos.Namespaces{"default": neffos.Events{
		"block": func(c *neffos.NSConn, msg neffos.Message) error {
			entered <- c.Conn.ID()
			// releases is filled before any client dials, and only read here.
			<-releases[c.Conn.Server()]
			return nil
		},
	}})
	for _, srv := range ts.servers {
		releases[srv] = make(chan struct{})
	}
	t.Cleanup(func() {
		for _, ch := range releases {
			select {
			case <-ch:
			default:
				close(ch)
			}
		}
	})

	return ts, entered, releases
}

// enterBlock connects client to "default", emits "block" and waits until the
// server-side callback runs.
func enterBlock(t *testing.T, client *neffos.Client, entered chan string) {
	t.Helper()

	ns, err := client.Connect(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := ns.Send("block", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the block callback did not run")
	}
}

func TestServerShutdownWaitsForReaders(t *testing.T) {
	ts, entered, releases := blockingServer(t)

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
		srv := ts.servers[backend]
		enterBlock(t, client, entered)

		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done <- srv.Shutdown(ctx)
		}()

		select {
		case err := <-done:
			t.Fatalf("%s: Shutdown returned (%v) while a callback was still running", backend, err)
		case <-time.After(50 * time.Millisecond):
		}

		close(releases[srv])

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: expected a nil error, got %v", backend, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: Shutdown did not return after the callback finished", backend)
		}

		if n := srv.GetTotalConnections(); n != 0 {
			t.Fatalf("%s: expected no connections after Shutdown, got %d", backend, n)
		}
		select {
		case <-client.NotifyClose:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the client was not closed", backend)
		}
	})
}

func TestServerShutdownContextTimeout(t *testing.T) {
	ts, entered, _ := blockingServer(t)

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
		srv := ts.servers[backend]
		enterBlock(t, client, entered)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := srv.Shutdown(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: expected context.DeadlineExceeded, got %v", backend, err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("%s: Shutdown took %s, expected it to stop at the context deadline", backend, elapsed)
		}
	})
}

// recordingExchange is a StackExchange that records OnConnect and
// OnDisconnect calls and runs onConnect from inside OnConnect.
type recordingExchange struct {
	mu        sync.Mutex
	events    []string
	onConnect func()
}

func (e *recordingExchange) record(event string) {
	e.mu.Lock()
	e.events = append(e.events, event)
	e.mu.Unlock()
}

func (e *recordingExchange) recorded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.events...)
}

func (e *recordingExchange) OnConnect(*neffos.Conn) error {
	e.record("connect")
	if e.onConnect != nil {
		e.onConnect()
	}
	return nil
}

func (e *recordingExchange) OnDisconnect(*neffos.Conn)              { e.record("disconnect") }
func (e *recordingExchange) Publish([]neffos.Message) bool          { return true }
func (e *recordingExchange) Subscribe(*neffos.Conn, string)         {}
func (e *recordingExchange) Unsubscribe(*neffos.Conn, string)       {}
func (e *recordingExchange) NotifyAsk(neffos.Message, string) error { return nil }
func (e *recordingExchange) Ask(context.Context, neffos.Message, string) (neffos.Message, error) {
	return neffos.Message{}, nil
}

// Close lands while Upgrade is inside StackExchange.OnConnect, so the
// exchange hears OnDisconnect (from Close) before OnConnect returns. Upgrade
// must then skip the user's OnConnect, tell the exchange again, and return
// ErrServerClosed.
func TestUpgradeCloseDuringExchangeOnConnect(t *testing.T) {
	for backend, upgrader := range upgraders {
		srv := neffos.New(upgrader, neffos.Namespaces{"default": neffos.Events{}})
		exc := &recordingExchange{onConnect: srv.Close}
		srv.StackExchange = exc

		var userOnConnect atomic.Bool
		srv.OnConnect = func(*neffos.Conn) error {
			userOnConnect.Store(true)
			return nil
		}

		results := make(chan error, 1)
		httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := srv.Upgrade(w, r, nil, nil)
			results <- err
		}))

		url := "ws://" + strings.TrimPrefix(httpServer.URL, "http://")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if client, err := neffos.Dial(ctx, dialers[backend], url, neffos.Namespaces{"default": neffos.Events{}}); err == nil {
			client.Close()
		}
		cancel()

		select {
		case err := <-results:
			if !errors.Is(err, neffos.ErrServerClosed) {
				t.Errorf("%s: expected ErrServerClosed, got %v", backend, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: Upgrade did not return", backend)
		}
		httpServer.Close()

		if userOnConnect.Load() {
			t.Errorf("%s: expected the user OnConnect not to run on a closed server", backend)
		}
		events := exc.recorded()
		if len(events) == 0 || events[len(events)-1] != "disconnect" {
			t.Errorf("%s: expected the exchange to end on disconnect, got %v", backend, events)
		}
	}
}

// TestHeartbeatKeepsIdleConnectionAlive leaves a connection idle for longer
// than the server's ReadTimeout. The server's pings and the client's pongs
// keep it open.
func TestHeartbeatKeepsIdleConnectionAlive(t *testing.T) {
	ts := newTestServers(t, neffos.WithTimeout{
		ReadTimeout:  300 * time.Millisecond,
		PingInterval: 100 * time.Millisecond,
		Namespaces:   neffos.Namespaces{"default": neffos.Events{}},
	})

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
		time.Sleep(time.Second)

		if client.Conn().IsClosed() {
			t.Fatalf("%s: the client closed while idle: %v", backend, client.Conn().Err())
		}
		if n := ts.servers[backend].GetTotalConnections(); n != 1 {
			t.Fatalf("%s: expected 1 server connection after the idle second, got %d", backend, n)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := client.Connect(ctx, "default"); err != nil {
			t.Fatalf("%s: connect after idling: %v", backend, err)
		}
	})
}

// TestMaxMessageSizeClosesWith1009 sends a message over the server's
// MaxMessageSize. The server reports ErrMessageTooBig and the client sees
// close code 1009.
func TestMaxMessageSizeClosesWith1009(t *testing.T) {
	ts := newTestServers(t, neffos.WithTimeout{
		MaxMessageSize: 64,
		Namespaces:     neffos.Namespaces{"default": neffos.Events{}},
	})

	serverErrs := make(map[string]chan error, len(ts.servers))
	for backend, srv := range ts.servers {
		errs := make(chan error, 1)
		serverErrs[backend] = errs
		srv.OnDisconnect = func(c *neffos.Conn) { errs <- c.Err() }
	}

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
		if err := client.Conn().Socket().WriteBinary(make([]byte, 1024), time.Second); err != nil {
			t.Fatalf("%s: write: %v", backend, err)
		}

		select {
		case <-client.NotifyClose:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the client was not closed", backend)
		}
		if got := neffos.CloseStatus(client.Conn().Err()); got != neffos.CloseMessageTooBig {
			t.Fatalf("%s: expected close code %d on the client, got %d (%v)", backend, neffos.CloseMessageTooBig, got, client.Conn().Err())
		}

		select {
		case err := <-serverErrs[backend]:
			if !errors.Is(err, neffos.ErrMessageTooBig) {
				t.Fatalf("%s: expected ErrMessageTooBig on the server, got %v", backend, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the server connection was not closed", backend)
		}
	})
}

// TestCloseStatusReachesOnDisconnect checks that the code and reason passed
// to Terminate reach the other side, in both directions.
func TestCloseStatusReachesOnDisconnect(t *testing.T) {
	expectStatus := func(t *testing.T, side string, err error, code int, reason string) {
		t.Helper()

		ce, ok := errors.AsType[neffos.CloseError](err)
		if !ok || neffos.CloseStatus(err) != code || ce.Reason != reason {
			t.Fatalf("%s: expected [%d] %q, got %v", side, code, reason, err)
		}
	}

	t.Run("client to server", func(t *testing.T) {
		ts := newTestServers(t, neffos.Namespaces{"default": neffos.Events{}})

		serverErrs := make(map[string]chan error, len(ts.servers))
		for backend, srv := range ts.servers {
			errs := make(chan error, 1)
			serverErrs[backend] = errs
			srv.OnDisconnect = func(c *neffos.Conn) { errs <- c.Err() }
		}

		ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
			client.Conn().Terminate(neffos.ClosePolicyViolation, "bye")

			select {
			case err := <-serverErrs[backend]:
				expectStatus(t, backend+" server", err, neffos.ClosePolicyViolation, "bye")
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: server OnDisconnect did not fire", backend)
			}
		})
	})

	t.Run("server to client", func(t *testing.T) {
		ts := newTestServers(t, neffos.Namespaces{"default": neffos.Events{}})

		serverConns := make(map[string]chan *neffos.Conn, len(ts.servers))
		for backend, srv := range ts.servers {
			conns := make(chan *neffos.Conn, 1)
			serverConns[backend] = conns
			srv.OnConnect = func(c *neffos.Conn) error { conns <- c; return nil }
		}

		ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
			select {
			case c := <-serverConns[backend]:
				c.Terminate(4000, "kicked")
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: server OnConnect did not fire", backend)
			}

			select {
			case <-client.NotifyClose:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: the client was not closed", backend)
			}
			expectStatus(t, backend+" client", client.Conn().Err(), 4000, "kicked")
		})
	})
}

// TestAsyncBroadcastDeliversEveryMessage sends 50 broadcasts back to back while
// the client's handler sleeps on every message. The server keeps writing one
// broadcast while the next ones arrive, so a waiter that skipped a generation
// would lose messages here.
func TestAsyncBroadcastDeliversEveryMessage(t *testing.T) {
	const (
		namespace = "default"
		total     = 50
	)

	got := make(chan string, total*len(dialers))
	clientEvents := neffos.Namespaces{
		namespace: neffos.Events{
			"msg": func(c *neffos.NSConn, msg neffos.Message) error {
				time.Sleep(5 * time.Millisecond)
				got <- string(msg.Body)
				return nil
			},
		},
	}

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{}})

	ts.dial(t, clientEvents, func(backend string, client *neffos.Client) {
		if _, err := client.Connect(context.TODO(), namespace); err != nil {
			t.Fatalf("%s: %v", backend, err)
		}

		srv := ts.servers[backend]
		for i := range total {
			srv.Broadcast(nil, neffos.Message{Namespace: namespace, Event: "msg", Body: []byte(strconv.Itoa(i))})
		}

		var received []string
		timeout := time.After(10 * time.Second)
		for len(received) < total {
			select {
			case body := <-got:
				received = append(received, body)
			case <-timeout:
				t.Fatalf("%s: received %d of %d broadcasts: %v", backend, len(received), total, received)
			}
		}

		for i, body := range received {
			if want := strconv.Itoa(i); body != want {
				t.Fatalf("%s: message %d: expected body %q, got %q (all: %v)", backend, i, want, body, received)
			}
		}
	})
}

// TestBroadcastBatchWithDifferentTargets sends one Broadcast call that carries
// a message for "a" and a message for "b". Each connection must get its own,
// even though the other message in the batch is not addressed to it.
func TestBroadcastBatchWithDifferentTargets(t *testing.T) {
	const namespace = "default"

	got := make(chan string, 4*len(dialers))
	clientEvents := neffos.Namespaces{
		namespace: neffos.Events{
			"msg": func(c *neffos.NSConn, msg neffos.Message) error {
				got <- c.Conn.ID() + ":" + string(msg.Body)
				return nil
			},
		},
	}

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{}}, func(srv *neffos.Server) {
		var n atomic.Int32
		srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
			if n.Add(1) == 1 {
				return "a"
			}
			return "b"
		}
	})

	connect := func(backend string, client *neffos.Client) {
		if _, err := client.Connect(context.TODO(), namespace); err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
	}
	ts.dial(t, clientEvents, connect) // "a" on every backend.
	ts.dial(t, clientEvents, connect) // "b" on every backend.

	for backend, srv := range ts.servers {
		srv.Broadcast(nil,
			neffos.Message{Namespace: namespace, Event: "msg", To: "a", Body: []byte("for-a")},
			neffos.Message{Namespace: namespace, Event: "msg", To: "b", Body: []byte("for-b")},
		)

		received := make(map[string]bool)
		timeout := time.After(5 * time.Second)
		for len(received) < 2 {
			select {
			case s := <-got:
				received[s] = true
			case <-timeout:
				t.Fatalf("%s: expected a:for-a and b:for-b, got %v", backend, received)
			}
		}

		if !received["a:for-a"] || !received["b:for-b"] {
			t.Fatalf("%s: expected a:for-a and b:for-b, got %v", backend, received)
		}
	}
}
