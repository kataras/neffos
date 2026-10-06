package redis

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"

	gorillaws "github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
)

// requireBroker skips the test unless NEFFOS_REDIS_ADDR points to a redis
// server, and always under -short. When the variable is set it waits up to
// 10s for the server to answer a PING and fails, not skips, if it never
// does. It returns the address.
func requireBroker(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping redis broker test in -short mode")
	}

	addr := os.Getenv("NEFFOS_REDIS_ADDR")
	if addr == "" {
		t.Skip("NEFFOS_REDIS_ADDR is not set; skipping redis broker test")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		c := goredis.NewClient(&goredis.Options{Addr: addr, DialTimeout: time.Second, MaxRetries: -1})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := c.Ping(ctx).Err()
		cancel()
		c.Close()
		if err == nil {
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("redis at %s did not answer PING within 10s: %v", addr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestGetChannelEmptyNamespace(t *testing.T) {
	exc := &StackExchange{channel: "neffos"}

	for _, room := range []string{"", "room"} {
		var got string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("getChannel(\"\", %q, \"\") panicked: %v", room, r)
				}
			}()
			got = exc.getChannel("", room, "")
		}()

		if got == "" || !strings.HasPrefix(got, "neffos.") {
			t.Fatalf("getChannel(\"\", %q, \"\"): expected a channel under the prefix, got %q", room, got)
		}
	}
}

// TestAskAfterCloseReturnsErrClosed pins the errClosed doc: Ask after Close
// returns errClosed and does not touch the redis client (nil here).
func TestAskAfterCloseReturnsErrClosed(t *testing.T) {
	exc := &StackExchange{channel: "neffos", closed: true}

	_, err := exc.Ask(context.Background(), neffos.Message{Namespace: "default", Event: "ask"}, "token")
	if err != errClosed {
		t.Fatalf("expected errClosed, got %v", err)
	}
}

func TestChannelNames(t *testing.T) {
	exc := &StackExchange{channel: "neffos"}

	tests := []struct {
		name                  string
		namespace, room, conn string
		expected              string
	}{
		{"namespace", "default", "", "", "neffos.default."},
		{"room goes to its namespace", "default", "room1", "", "neffos.default."},
		{"empty namespace", "", "", "", "neffos.."},
		{"glob characters kept as is", "a*b?[c]", "", "", "neffos.a*b?[c]."},
		{"connection", "default", "room1", "conn-id", "neffos.conn-id."},
	}

	for _, tt := range tests {
		if got := exc.getChannel(tt.namespace, tt.room, tt.conn); got != tt.expected {
			t.Errorf("%s: getChannel(%q, %q, %q): expected %q, got %q", tt.name, tt.namespace, tt.room, tt.conn, tt.expected, got)
		}
	}

	if got, expected := exc.askChannel("k2x9-1f-0123abcd"), "neffos.ask.k2x9-1f-0123abcd"; got != expected {
		t.Errorf("askChannel: expected %q, got %q", expected, got)
	}
}

func TestNewStackExchangeBadAddressFails(t *testing.T) {
	// Port 1 on loopback is closed on any sane machine, so the eager ping fails fast.
	exc, err := NewStackExchange(Config{Addr: "127.0.0.1:1", DialTimeout: time.Second}, "neffos")
	if err == nil {
		exc.Close()
		t.Fatal("expected an error for an unreachable redis address")
	}
}

// TestOnConnectBoundedByDialTimeout pins that a redis which accepts the
// connection but never answers cannot hold OnConnect, and with it the
// websocket handshake, for longer than about DialTimeout.
func TestOnConnectBoundedByDialTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	defer func() {
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c) // accept and never answer.
			mu.Unlock()
		}
	}()

	const dialTimeout = 200 * time.Millisecond

	// NewStackExchange fails at its eager Ping here, so build the exchange by hand.
	exc := &StackExchange{
		channel:     "neffos",
		dialTimeout: dialTimeout,
		client:      newClient(Config{Network: "tcp", Addr: ln.Addr().String(), DialTimeout: dialTimeout, MaxActive: 10}),
		subscribers: make(map[*neffos.Conn]*goredis.PubSub),
	}
	defer exc.Close()

	errCh := make(chan error, 1)
	start := time.Now()
	go func() { errCh <- exc.OnConnect(new(neffos.Conn)) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected OnConnect to fail against a redis that never answers")
		}
		if elapsed := time.Since(start); elapsed > 5*dialTimeout {
			t.Fatalf("OnConnect returned after %s, expected about %s", elapsed, dialTimeout)
		}
	case <-time.After(10 * dialTimeout):
		t.Fatalf("OnConnect did not return within %s", 10*dialTimeout)
	}
}

// testEnv is a neffos server on httptest that uses a redis StackExchange.
type testEnv struct {
	exc *StackExchange
	srv *neffos.Server
	ts  *httptest.Server
	url string
}

func newTestEnv(t *testing.T, addr, prefix string, events neffos.Namespaces) *testEnv {
	t.Helper()

	exc, err := NewStackExchange(Config{Addr: addr}, prefix)
	if err != nil {
		t.Fatalf("NewStackExchange: %v", err)
	}

	srv := neffos.New(gorilla.DefaultUpgrader, events)
	if err := srv.UseStackExchange(exc); err != nil {
		t.Fatalf("UseStackExchange: %v", err)
	}

	ts := httptest.NewServer(srv)
	env := &testEnv{exc: exc, srv: srv, ts: ts, url: "ws" + strings.TrimPrefix(ts.URL, "http")}
	t.Cleanup(env.close)
	return env
}

func (e *testEnv) close() {
	e.srv.Close()
	e.ts.Close()
}

// uniquePrefix keeps parallel or repeated runs on the same redis apart.
func uniquePrefix(t *testing.T) string {
	return "neffos-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000000000")
}

func newRedisChecker(t *testing.T, addr string) *goredis.Client {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { c.Close() })
	return c
}

// waitNumSub waits until channel has exactly n subscribers on the broker.
func waitNumSub(t *testing.T, rc *goredis.Client, channel string, n int64) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		res, err := rc.PubSubNumSub(context.Background(), channel).Result()
		if err != nil {
			t.Fatalf("PUBSUB NUMSUB %s: %v", channel, err)
		}
		if got = res[channel]; got == n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("channel %q: expected %d subscribers, got %d", channel, n, got)
}

func dialClient(t *testing.T, url string, events neffos.Namespaces) *neffos.Client {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, url, events)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

func connectNamespace(t *testing.T, client *neffos.Client, namespace string) *neffos.NSConn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ns, err := client.Connect(ctx, namespace)
	if err != nil {
		t.Fatalf("Connect(%q): %v", namespace, err)
	}
	return ns
}

func serverEvents(namespace string) neffos.Namespaces {
	return neffos.Namespaces{namespace: neffos.Events{}}
}

func testBroadcastAcrossServers(t *testing.T, namespace, room string) {
	addr := requireBroker(t)
	prefix := uniquePrefix(t)

	s1 := newTestEnv(t, addr, prefix, serverEvents(namespace))
	s2 := newTestEnv(t, addr, prefix, serverEvents(namespace))

	received := make(chan string, 4)
	client := dialClient(t, s2.url, neffos.Namespaces{namespace: neffos.Events{
		"chat": func(_ *neffos.NSConn, msg neffos.Message) error {
			received <- string(msg.Body)
			return nil
		},
	}})
	ns := connectNamespace(t, client, namespace)
	if room != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := ns.JoinRoom(ctx, room)
		cancel()
		if err != nil {
			t.Fatalf("JoinRoom: %v", err)
		}
	}

	waitNumSub(t, newRedisChecker(t, addr), s1.exc.getChannel(namespace, "", ""), 1)

	s1.srv.Broadcast(nil, neffos.Message{Namespace: namespace, Room: room, Event: "chat", Body: []byte("hello")})

	select {
	case got := <-received:
		if got != "hello" {
			t.Fatalf("expected body %q, got %q", "hello", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client on server 2 did not receive the broadcast made on server 1")
	}
}

func TestBroadcastAcrossServers(t *testing.T) {
	t.Run("namespace", func(t *testing.T) { testBroadcastAcrossServers(t, "default", "") })
	t.Run("room", func(t *testing.T) { testBroadcastAcrossServers(t, "default", "room1") })
}

func TestNamespaceWithGlobChars(t *testing.T) {
	testBroadcastAcrossServers(t, "a*b?[c]", "")
}

// rawClient speaks the neffos wire protocol over a plain gorilla websocket, so
// a test can control exactly which wait token goes back to the server.
type rawClient struct {
	t    *testing.T
	conn *gorillaws.Conn
	id   string
}

func dialRaw(t *testing.T, url string) *rawClient {
	t.Helper()

	conn, _, err := gorillaws.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("raw dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	c := &rawClient{t: t, conn: conn}
	c.write("M")
	ack := c.mustRead()
	if !strings.HasPrefix(ack, "A") {
		t.Fatalf("raw client: expected the ack with the connection ID, got %q", ack)
	}
	c.id = ack[1:]
	return c
}

func (c *rawClient) write(s string) {
	c.t.Helper()
	if err := c.conn.WriteMessage(gorillaws.TextMessage, []byte(s)); err != nil {
		c.t.Fatalf("raw write: %v", err)
	}
}

// read returns the next frame. It does not touch t, so it is safe to call
// from a goroutine other than the test's.
func (c *rawClient) read() (string, error) {
	c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, b, err := c.conn.ReadMessage()
	if err != nil {
		return "", fmt.Errorf("raw read: %w", err)
	}
	return string(b), nil
}

func (c *rawClient) mustRead() string {
	c.t.Helper()
	s, err := c.read()
	if err != nil {
		c.t.Fatal(err)
	}
	return s
}

// connect joins namespace and waits for the server's reply.
func (c *rawClient) connect(namespace string) {
	c.t.Helper()
	c.write("$1;" + namespace + ";;_OnNamespaceConnect;0;0;")
	if reply := c.mustRead(); !strings.HasPrefix(reply, "$1;") {
		c.t.Fatalf("raw connect: unexpected reply %q", reply)
	}
}

// answer reads one frame (fields: wait;namespace;room;event;isError;isNoOp;body)
// and replies to it with body, sending back the wait token that mapWait returns.
// It returns an error instead of failing t, because it runs on its own goroutine.
func (c *rawClient) answer(mapWait func(string) string, body string) error {
	frame, err := c.read()
	if err != nil {
		return err
	}
	fields := strings.SplitN(frame, ";", 7)
	if len(fields) != 7 || fields[0] == "" {
		return fmt.Errorf("raw answer: expected a frame with a wait token, got %q", frame)
	}
	reply := strings.Join([]string{mapWait(fields[0]), fields[1], fields[2], fields[3], "0", "0", body}, ";")
	if err := c.conn.WriteMessage(gorillaws.TextMessage, []byte(reply)); err != nil {
		return fmt.Errorf("raw write: %w", err)
	}
	return nil
}

func TestServerAskAcrossServers(t *testing.T) {
	const namespace = "default"

	ask := func(t *testing.T, s2 *testEnv, to string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		reply, err := s2.srv.Ask(ctx, neffos.Message{Namespace: namespace, Event: "ask", To: to, Body: []byte("ping")})
		if err != nil {
			t.Fatalf("Server.Ask: %v", err)
		}
		if string(reply.Body) != "pong" {
			t.Fatalf("expected reply body %q, got %q", "pong", reply.Body)
		}
	}

	t.Run("go client", func(t *testing.T) {
		addr := requireBroker(t)
		prefix := uniquePrefix(t)
		s1 := newTestEnv(t, addr, prefix, serverEvents(namespace))
		s2 := newTestEnv(t, addr, prefix, serverEvents(namespace))

		client := dialClient(t, s1.url, neffos.Namespaces{namespace: neffos.Events{
			"ask": func(_ *neffos.NSConn, msg neffos.Message) error {
				return neffos.Reply([]byte("pong"))
			},
		}})
		connectNamespace(t, client, namespace)

		ask(t, s2, client.Conn().ID())
	})

	// The two raw clients pin that the answer does not depend on the kind of
	// client: the server strips the marker before forwarding, so neither ever sees a '!'.
	rawCases := []struct {
		name    string
		mapWait func(string) string
	}{
		// neffos.js sends the wait token back exactly as it arrived.
		{"verbatim echo", func(w string) string { return w }},
		// a v0.0.x Go client removes the stack exchange marker ('!' as the second character).
		{"marker stripping", func(w string) string {
			if len(w) > 2 && w[1] == '!' {
				return w[:1] + w[2:]
			}
			return w
		}},
	}

	for _, tt := range rawCases {
		t.Run(tt.name, func(t *testing.T) {
			addr := requireBroker(t)
			prefix := uniquePrefix(t)
			s1 := newTestEnv(t, addr, prefix, serverEvents(namespace))
			s2 := newTestEnv(t, addr, prefix, serverEvents(namespace))

			raw := dialRaw(t, s1.url)
			raw.connect(namespace)

			answerErr := make(chan error, 1)
			go func() { answerErr <- raw.answer(tt.mapWait, "pong") }()

			ask(t, s2, raw.id)
			if err := <-answerErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	addr := requireBroker(t)
	const namespace = "default"
	prefix := uniquePrefix(t)

	s1 := newTestEnv(t, addr, prefix, serverEvents(namespace))
	rc := newRedisChecker(t, addr)

	var received atomic.Int32
	client := dialClient(t, s1.url, neffos.Namespaces{namespace: neffos.Events{
		"chat": func(*neffos.NSConn, neffos.Message) error {
			received.Add(1)
			return nil
		},
	}})
	ns := connectNamespace(t, client, namespace)

	nsChannel := s1.exc.getChannel(namespace, "", "")
	connChannel := s1.exc.getChannel("", "", client.Conn().ID())
	waitNumSub(t, rc, nsChannel, 1)
	waitNumSub(t, rc, connChannel, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ns.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// The namespace channel is released, the connection's own channel is not.
	waitNumSub(t, rc, nsChannel, 0)
	waitNumSub(t, rc, connChannel, 1)

	if err := rc.Publish(ctx, nsChannel, neffos.Message{Namespace: namespace, Event: "chat"}.Serialize()).Err(); err != nil {
		t.Fatalf("PUBLISH: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := received.Load(); n != 0 {
		t.Fatalf("expected no delivery after unsubscribe, got %d", n)
	}

	// OnDisconnect releases the connection's own subscriber.
	client.Close()
	waitNumSub(t, rc, connChannel, 0)
}

func TestCloseIdempotentAndReleases(t *testing.T) {
	addr := requireBroker(t)
	const namespace = "default"

	baseline := runtime.NumGoroutine()

	func() {
		exc, err := NewStackExchange(Config{Addr: addr}, uniquePrefix(t))
		if err != nil {
			t.Fatalf("NewStackExchange: %v", err)
		}

		srv := neffos.New(gorilla.DefaultUpgrader, serverEvents(namespace))
		if err := srv.UseStackExchange(exc); err != nil {
			t.Fatalf("UseStackExchange: %v", err)
		}
		ts := httptest.NewServer(srv)
		url := "ws" + strings.TrimPrefix(ts.URL, "http")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for i := 0; i < 3; i++ {
			client, err := neffos.Dial(ctx, gorilla.DefaultDialer, url, serverEvents(namespace))
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			if _, err := client.Connect(ctx, namespace); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer client.Close()
		}

		if err := exc.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := exc.Close(); err != nil {
			t.Fatalf("second Close: expected nil, got %v", err)
		}

		srv.Close()
		ts.Close()
	}()

	deadline := time.Now().Add(2 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		if n = runtime.NumGoroutine(); n <= baseline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	buf := make([]byte, 1<<16)
	t.Fatalf("goroutines did not return to baseline %d within 2s, got %d:\n%s", baseline, n, buf[:runtime.Stack(buf, true)])
}
