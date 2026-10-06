package nats

import (
	"bufio"
	"context"
	"errors"
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
	"github.com/nats-io/nats.go"
)

// requireBroker skips the test unless NEFFOS_NATS_URL points to a nats
// server, and always under -short. When the variable is set it waits up to
// 10s for the server to accept a connection (CI starts nats without a health
// check) and fails, not skips, if it never does. It returns the URL.
func requireBroker(t *testing.T) string {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping nats broker test in -short mode")
	}

	url := os.Getenv("NEFFOS_NATS_URL")
	if url == "" {
		t.Skip("NEFFOS_NATS_URL is not set; skipping nats broker test")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		nc, err := nats.Connect(url, nats.Timeout(time.Second))
		if err == nil {
			nc.Close()
			return url
		}
		if time.Now().After(deadline) {
			t.Fatalf("nats at %s did not accept a connection within 10s: %v", url, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestSubjectNames(t *testing.T) {
	exc := &StackExchange{SubjectPrefix: "neffos"}

	tests := []struct {
		name                  string
		namespace, room, conn string
		expected              string
	}{
		{"namespace", "default", "", "", "neffos.default"},
		{"room goes to its namespace", "default", "room1", "", "neffos.default"},
		{"connection", "default", "room1", "conn-id", "neffos.conn-id"},
	}

	for _, tt := range tests {
		if got := exc.getSubject(tt.namespace, tt.room, tt.conn); got != tt.expected {
			t.Errorf("%s: getSubject(%q, %q, %q): expected %q, got %q", tt.name, tt.namespace, tt.room, tt.conn, tt.expected, got)
		}
	}

	if got, expected := exc.askSubject("k2x9-1f-0123abcd"), "neffos.ask.k2x9-1f-0123abcd"; got != expected {
		t.Errorf("askSubject: expected %q, got %q", expected, got)
	}
}

func TestSubjectSanitize(t *testing.T) {
	exc := &StackExchange{SubjectPrefix: "neffos"}

	tests := []struct {
		name                  string
		namespace, room, conn string
		expected              string
	}{
		{"empty namespace", "", "", "", "neffos._"},
		{"empty namespace with a room", "", "room1", "", "neffos._"},
		{"dots", "a.b.c", "", "", "neffos.a_b_c"},
		{"star wildcard", "a*b", "", "", "neffos.a_b"},
		{"lone star", "*", "", "", "neffos._"},
		{"full wildcard", ">", "", "", "neffos._"},
		{"spaces", "a b", "", "", "neffos.a_b"},
		{"tab and newline", "a\tb\nc", "", "", "neffos.a_b_c"},
		{"connection with a dot", "", "", "x.y", "neffos.x_y"},
	}

	for _, tt := range tests {
		var got string
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s: getSubject(%q, %q, %q) panicked: %v", tt.name, tt.namespace, tt.room, tt.conn, r)
				}
			}()
			got = exc.getSubject(tt.namespace, tt.room, tt.conn)
		}()

		if got != tt.expected {
			t.Errorf("%s: getSubject(%q, %q, %q): expected %q, got %q", tt.name, tt.namespace, tt.room, tt.conn, tt.expected, got)
		}
	}

	if got, expected := exc.askSubject("a.b*c>d e"), "neffos.ask.a_b_c_d_e"; got != expected {
		t.Errorf("askSubject: expected %q, got %q", expected, got)
	}
	if got, expected := exc.askSubject(""), "neffos.ask._"; got != expected {
		t.Errorf("askSubject(\"\"): expected %q, got %q", expected, got)
	}
}

// silentListener accepts TCP connections and hands each one to serve, which
// runs on its own goroutine. Everything is closed by the test's cleanup.
func silentListener(t *testing.T, serve func(net.Conn)) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var (
		mu    sync.Mutex
		conns []net.Conn
		wg    sync.WaitGroup
	)
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})

	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			if serve != nil {
				wg.Go(func() {
					serve(c)
				})
			}
		}
	})

	return "nats://" + ln.Addr().String()
}

// TestNewStackExchangeSilentServerFails pins that a server which accepts the
// TCP connection but never speaks fails NewStackExchange within the connect
// timeout instead of hanging.
func TestNewStackExchangeSilentServerFails(t *testing.T) {
	url := silentListener(t, nil)
	const timeout = 200 * time.Millisecond

	type result struct {
		exc *StackExchange
		err error
	}
	resCh := make(chan result, 1)
	start := time.Now()
	go func() {
		exc, err := NewStackExchange(url, nats.Timeout(timeout), nats.NoReconnect())
		resCh <- result{exc, err}
	}()

	select {
	case res := <-resCh:
		if res.err == nil {
			res.exc.Close()
			t.Fatal("expected NewStackExchange to fail against a server that never answers")
		}
		if elapsed := time.Since(start); elapsed > 5*timeout {
			t.Fatalf("NewStackExchange returned after %s, expected about %s", elapsed, timeout)
		}
	case <-time.After(10 * timeout):
		t.Fatalf("NewStackExchange did not return within %s", 10*timeout)
	}
}

// serveConnectThenStall plays a nats server just long enough for the client
// to connect (INFO, then PONG to the first PING) and then never answers again.
func serveConnectThenStall(c net.Conn) {
	if _, err := c.Write([]byte(`INFO {"server_id":"stall","version":"2.10.0","proto":1,"max_payload":1048576,"headers":true}` + "\r\n")); err != nil {
		return
	}
	r := bufio.NewReader(c)
	answered := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if !answered && strings.HasPrefix(line, "PING") {
			answered = true
			if _, err := c.Write([]byte("PONG\r\n")); err != nil {
				return
			}
		}
	}
}

// TestOnConnectBoundedByTimeout pins that a nats server which stops
// answering cannot hold OnConnect, and with it the websocket handshake, for
// longer than about the nats connect timeout.
func TestOnConnectBoundedByTimeout(t *testing.T) {
	url := silentListener(t, serveConnectThenStall)
	const timeout = 200 * time.Millisecond

	exc, err := NewStackExchange(url, nats.Timeout(timeout), nats.NoReconnect())
	if err != nil {
		t.Fatalf("NewStackExchange: %v", err)
	}
	t.Cleanup(func() {
		// Close the nats connections directly: draining against a server
		// that never answers would wait out the drain flush.
		exc.publisher.Close()
		exc.subscriber.Close()
		exc.Close()
	})

	errCh := make(chan error, 1)
	start := time.Now()
	go func() { errCh <- exc.OnConnect(new(neffos.Conn)) }()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected OnConnect to fail against a nats server that never answers")
		}
		if !errors.Is(err, nats.ErrTimeout) {
			t.Fatalf("expected OnConnect to give up on the flush timeout, got %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*timeout {
			t.Fatalf("OnConnect returned after %s, expected about %s", elapsed, timeout)
		}
	case <-time.After(10 * timeout):
		t.Fatalf("OnConnect did not return within %s", 10*timeout)
	}
}

// testEnv is a neffos server on httptest that uses a nats StackExchange.
type testEnv struct {
	exc *StackExchange
	srv *neffos.Server
	ts  *httptest.Server
	url string
}

func newTestEnv(t *testing.T, natsURL, prefix string, events neffos.Namespaces) *testEnv {
	t.Helper()

	exc, err := NewStackExchange(natsURL)
	if err != nil {
		t.Fatalf("NewStackExchange: %v", err)
	}
	exc.SubjectPrefix = prefix

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

// uniquePrefix keeps parallel or repeated runs on the same nats apart.
func uniquePrefix(t *testing.T) string {
	return "neffos-test-" + strings.ReplaceAll(t.Name(), "/", "-") + "-" + time.Now().Format("150405.000000000")
}

// subscription returns the exchange's subscription to subject held for the
// connection with the given ID, or nil.
func (exc *StackExchange) subscription(connID, subject string) *nats.Subscription {
	exc.mu.Lock()
	defer exc.mu.Unlock()
	for c, subs := range exc.subscriptions {
		if c.ID() == connID {
			return subs[subject]
		}
	}
	return nil
}

// hasConn reports whether the exchange still tracks the connection with the given ID.
func (exc *StackExchange) hasConn(connID string) bool {
	exc.mu.Lock()
	defer exc.mu.Unlock()
	for c := range exc.subscriptions {
		if c.ID() == connID {
			return true
		}
	}
	return false
}

// waitSubscribed waits until the exchange holds a subscription to subject for
// the connection, then flushes the subscriber so the nats server has
// registered it before the test publishes.
func waitSubscribed(t *testing.T, exc *StackExchange, connID, subject string) *nats.Subscription {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sub := exc.subscription(connID, subject); sub != nil {
			if err := exc.subscriber.FlushTimeout(2 * time.Second); err != nil {
				t.Fatalf("flush subscriber: %v", err)
			}
			return sub
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("connection %s: no subscription to %q", connID, subject)
	return nil
}

// waitUnsubscribed waits until the exchange no longer holds a subscription to
// subject for the connection.
func waitUnsubscribed(t *testing.T, exc *StackExchange, connID, subject string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if exc.subscription(connID, subject) == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("connection %s: still subscribed to %q", connID, subject)
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
	natsURL := requireBroker(t)
	prefix := uniquePrefix(t)

	s1 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))
	s2 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))

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

	waitSubscribed(t, s2.exc, client.Conn().ID(), s2.exc.getSubject(namespace, "", ""))

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
	t.Run("namespace with subject characters", func(t *testing.T) { testBroadcastAcrossServers(t, "a.b*c >", "") })
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
		natsURL := requireBroker(t)
		prefix := uniquePrefix(t)
		s1 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))
		s2 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))

		client := dialClient(t, s1.url, neffos.Namespaces{namespace: neffos.Events{
			"ask": func(_ *neffos.NSConn, msg neffos.Message) error {
				return neffos.Reply([]byte("pong"))
			},
		}})
		connectNamespace(t, client, namespace)
		waitSubscribed(t, s1.exc, client.Conn().ID(), s1.exc.getSubject("", "", client.Conn().ID()))

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
			natsURL := requireBroker(t)
			prefix := uniquePrefix(t)
			s1 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))
			s2 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))

			raw := dialRaw(t, s1.url)
			raw.connect(namespace)
			waitSubscribed(t, s1.exc, raw.id, s1.exc.getSubject("", "", raw.id))

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
	natsURL := requireBroker(t)
	const namespace = "default"
	prefix := uniquePrefix(t)

	s1 := newTestEnv(t, natsURL, prefix, serverEvents(namespace))

	var received atomic.Int32
	client := dialClient(t, s1.url, neffos.Namespaces{namespace: neffos.Events{
		"chat": func(*neffos.NSConn, neffos.Message) error {
			received.Add(1)
			return nil
		},
	}})
	ns := connectNamespace(t, client, namespace)
	connID := client.Conn().ID()

	nsSubject := s1.exc.getSubject(namespace, "", "")
	connSubject := s1.exc.getSubject("", "", connID)
	nsSub := waitSubscribed(t, s1.exc, connID, nsSubject)
	connSub := waitSubscribed(t, s1.exc, connID, connSubject)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ns.Disconnect(ctx); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	// The namespace subscription is released, the connection's own one is not.
	waitUnsubscribed(t, s1.exc, connID, nsSubject)
	if nsSub.IsValid() {
		t.Fatal("expected the namespace subscription to be unsubscribed")
	}
	if !connSub.IsValid() {
		t.Fatal("expected the connection subscription to stay valid")
	}

	checker, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats checker: %v", err)
	}
	defer checker.Close()
	if err := checker.Publish(nsSubject, neffos.Message{Namespace: namespace, Event: "chat"}.Serialize()); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := checker.FlushTimeout(2 * time.Second); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := received.Load(); n != 0 {
		t.Fatalf("expected no delivery after unsubscribe, got %d", n)
	}

	// OnDisconnect releases the connection's own subscription and its entry.
	client.Close()
	deadline := time.Now().Add(5 * time.Second)
	for s1.exc.hasConn(connID) {
		if time.Now().After(deadline) {
			t.Fatal("the exchange still tracks the connection after it closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if connSub.IsValid() {
		t.Fatal("expected the connection subscription to be unsubscribed after the connection closed")
	}
}

// TestCloseIdempotentAndReleases closes the exchanges of two servers after
// connections and Asks, and checks that Close is idempotent and that no
// goroutine is left behind. The Ask without a target is answered by both
// clients, so two replies arrive for one Ask: the old exchange blocked its
// nats callback forever on the second reply.
func TestCloseIdempotentAndReleases(t *testing.T) {
	natsURL := requireBroker(t)
	const namespace = "default"

	baseline := runtime.NumGoroutine()

	func() {
		prefix := uniquePrefix(t)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		type server struct {
			exc *StackExchange
			srv *neffos.Server
			ts  *httptest.Server
			url string
		}
		start := func() server {
			exc, err := NewStackExchange(natsURL)
			if err != nil {
				t.Fatalf("NewStackExchange: %v", err)
			}
			exc.SubjectPrefix = prefix

			srv := neffos.New(gorilla.DefaultUpgrader, serverEvents(namespace))
			if err := srv.UseStackExchange(exc); err != nil {
				t.Fatalf("UseStackExchange: %v", err)
			}
			ts := httptest.NewServer(srv)
			return server{exc, srv, ts, "ws" + strings.TrimPrefix(ts.URL, "http")}
		}
		s1, s2 := start(), start()

		answering := neffos.Namespaces{namespace: neffos.Events{
			"ask": func(*neffos.NSConn, neffos.Message) error { return neffos.Reply([]byte("pong")) },
		}}
		for i := range 3 {
			events := serverEvents(namespace)
			if i < 2 {
				events = answering
			}
			client, err := neffos.Dial(ctx, gorilla.DefaultDialer, s1.url, events)
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer client.Close()
			if _, err := client.Connect(ctx, namespace); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			waitSubscribed(t, s1.exc, client.Conn().ID(), s1.exc.getSubject(namespace, "", ""))
		}

		// Two clients answer, so the asking exchange gets two replies.
		askCtx, askCancel := context.WithTimeout(ctx, 2*time.Second)
		reply, err := s2.srv.Ask(askCtx, neffos.Message{Namespace: namespace, Event: "ask"})
		askCancel()
		if err != nil {
			t.Fatalf("Server.Ask: %v", err)
		}
		if string(reply.Body) != "pong" {
			t.Fatalf("expected reply body %q, got %q", "pong", reply.Body)
		}

		// A few Asks nobody answers. The old exchange opened a nats
		// connection for each one.
		for range 3 {
			askCtx, askCancel := context.WithTimeout(ctx, 100*time.Millisecond)
			_, err := s2.srv.Ask(askCtx, neffos.Message{Namespace: namespace, Event: "ask", To: "nobody"})
			askCancel()
			if err == nil {
				t.Fatal("expected an Ask nobody answers to fail")
			}
		}

		// Let the second reply reach the asking exchange before closing.
		time.Sleep(100 * time.Millisecond)

		for _, s := range []server{s1, s2} {
			if err := s.exc.Close(); err != nil {
				t.Fatalf("first Close: %v", err)
			}
			if err := s.exc.Close(); err != nil {
				t.Fatalf("second Close: expected nil, got %v", err)
			}
			s.srv.Close()
			s.ts.Close()
		}
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

// TestAskStartsNoGoroutine pins that an Ask in flight costs no goroutine and
// no nats connection in the exchange. The old exchange opened a nats
// connection (with its reader and flusher goroutines) and a callback
// goroutine for every Ask.
//
// It counts only goroutines that run nats or this package's code, so
// unrelated goroutines (test runner, http server, race detector) do not
// move the numbers.
func TestAskStartsNoGoroutine(t *testing.T) {
	natsURL := requireBroker(t)
	const namespace = "default"

	s := newTestEnv(t, natsURL, uniquePrefix(t), serverEvents(namespace))

	// Poll until the count is the same three samples in a row.
	steady, same := natsGoroutines(), 0
	for deadline := time.Now().Add(2 * time.Second); same < 2 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		if n := natsGoroutines(); n == steady {
			same++
		} else {
			steady, same = n, 0
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.srv.Ask(ctx, neffos.Message{Namespace: namespace, Event: "ask", To: "nobody"})
		done <- err
	}()

	// Wait until the Ask is inside the exchange before counting.
	for deadline := time.Now().Add(2 * time.Second); !strings.Contains(allStacks(), "(*StackExchange).Ask"); {
		if time.Now().After(deadline) {
			t.Fatal("the Ask never reached the exchange")
		}
		time.Sleep(time.Millisecond)
	}
	inFlight := natsGoroutines()
	stacks := allStacks()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expected an Ask nobody answers to fail")
	}

	// The goroutine that calls Ask is the only extra one expected; allow
	// slack of 2 for a nats internal goroutine that comes and goes.
	if extra := inFlight - steady; extra > 2 {
		t.Fatalf("an Ask in flight added %d nats goroutines, expected at most 2:\n%s", extra, stacks)
	}
}

// allStacks returns the stack of every goroutine.
func allStacks() string {
	buf := make([]byte, 1<<16)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// natsGoroutines counts the goroutines that run nats client code or this
// package's code.
func natsGoroutines() int {
	count := 0
	for g := range strings.SplitSeq(allStacks(), "\n\n") {
		if strings.Contains(g, "nats-io/nats.go") || strings.Contains(g, "neffos/stackexchange/nats.") {
			count++
		}
	}
	return count
}
