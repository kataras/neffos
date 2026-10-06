package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// startServer mounts newServer on its own httptest server, closes both when
// the test ends, and returns the server and its websocket URL.
func startServer(t *testing.T) (*neffos.Server, string) {
	t.Helper()

	ws := newServer()
	ts := httptest.NewServer(ws)
	t.Cleanup(ts.Close)
	t.Cleanup(ws.Close) // cleanups run last first: websockets close before the listener

	return ws, "ws" + strings.TrimPrefix(ts.URL, "http")
}

// testClient is a Go client in the chat namespace that records every event
// it receives into inbox. closed is the client's NotifyClose.
type testClient struct {
	*neffos.NSConn
	inbox  chan neffos.Message
	closed <-chan struct{}
}

// dial connects a test client with token and returns the error a real
// client would see.
func dial(t *testing.T, url, token string) (*testClient, error) {
	t.Helper()

	tc := &testClient{inbox: make(chan neffos.Message, 64)}
	record := func(c *neffos.NSConn, msg neffos.Message) error {
		tc.inbox <- msg
		return nil
	}
	events := neffos.Namespaces{
		namespace: neffos.Events{
			"Chat":    record,
			"Private": record,
			"Notice":  record,
			"Wave":    record,
			"Ping": func(c *neffos.NSConn, msg neffos.Message) error {
				return neffos.Reply([]byte("pong"))
			},
		},
	}

	ctx, cancel := deadline()
	defer cancel()

	header := http.Header{"Authorization": {"Bearer " + token}}
	client, err := neffos.Dial(ctx, gorilla.Dialer(&gorilla.Options{}, header), url, events)
	if err != nil {
		return nil, err
	}
	t.Cleanup(client.Close)
	tc.closed = client.NotifyClose

	tc.NSConn, err = client.Connect(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return tc, nil
}

// connect dials as name with the demo token and fails the test on error.
func connect(t *testing.T, url, name string) *testClient {
	t.Helper()

	tc, err := dial(t, url, name+"-token")
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tc
}

// expect waits up to two seconds for an event called event, skipping others.
func (tc *testClient) expect(t *testing.T, event string) neffos.Message {
	t.Helper()

	timeout := time.After(2 * time.Second)
	for {
		select {
		case msg := <-tc.inbox:
			if msg.Event == event {
				return msg
			}
		case <-timeout:
			t.Fatalf("%s: no %s event within two seconds", tc.Conn.ID(), event)
			return neffos.Message{}
		}
	}
}

// expectNone fails if an event called event arrives within a short quiet period.
func (tc *testClient) expectNone(t *testing.T, event string) {
	t.Helper()

	quiet := time.After(300 * time.Millisecond)
	for {
		select {
		case msg := <-tc.inbox:
			if msg.Event == event {
				t.Fatalf("%s: unexpected %s event: %s", tc.Conn.ID(), event, msg.Body)
			}
		case <-quiet:
			return
		}
	}
}

// expectClose waits up to two seconds for the connection to close and
// returns the error that closed it.
func (tc *testClient) expectClose(t *testing.T) error {
	t.Helper()

	select {
	case <-tc.closed:
		return tc.Conn.Err()
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: still open after two seconds", tc.Conn.ID())
		return nil
	}
}

// join joins room and fails the test on error.
func (tc *testClient) join(t *testing.T, room string) *neffos.Room {
	t.Helper()

	ctx, cancel := deadline()
	defer cancel()

	r, err := tc.JoinRoom(ctx, room)
	if err != nil {
		t.Fatalf("%s: join #%s: %v", tc.Conn.ID(), room, err)
	}
	return r
}

func chatOf(t *testing.T, msg neffos.Message) chatMessage {
	t.Helper()

	m, err := msg.As[chatMessage]()
	if err != nil {
		t.Fatalf("decode %s: %v", msg.Body, err)
	}
	return m
}

func TestBroadcast(t *testing.T) {
	_, url := startServer(t)
	alice := connect(t, url, "alice")
	bob := connect(t, url, "bob")

	// From is filled by the server, whatever the client claims.
	alice.SendObject("Chat", chatMessage{From: "mallory", Text: "hello"})
	if got := chatOf(t, bob.expect(t, "Chat")); got.From != "alice" || got.Text != "hello" {
		t.Fatalf("bob got %+v, want alice: hello", got)
	}
	alice.expectNone(t, "Chat") // BroadcastOthers skips the sender

	bob.SendObject("Private", chatMessage{To: "alice", Text: "psst"})
	if got := chatOf(t, alice.expect(t, "Private")); got.From != "bob" || got.Text != "psst" {
		t.Fatalf("alice got %+v, want bob: psst", got)
	}

	bob.SendObject("Private", chatMessage{To: "carol", Text: "hi"})
	if msg := bob.expect(t, "Private"); msg.Err == nil || !strings.Contains(msg.Err.Error(), "not online") {
		t.Fatalf("expected a Message.Err saying carol is not online, got %v", msg.Err)
	}
}

func TestRooms(t *testing.T) {
	_, url := startServer(t)
	alice := connect(t, url, "alice")
	bob := connect(t, url, "bob")
	carol := connect(t, url, "carol")

	general := alice.join(t, "general")
	bob.join(t, "general")

	general.SendObject("Chat", chatMessage{Text: "hi room"})
	msg := bob.expect(t, "Chat")
	if got := chatOf(t, msg); msg.Room != "general" || got.Text != "hi room" {
		t.Fatalf("bob got %q in #%s, want \"hi room\" in #general", got.Text, msg.Room)
	}
	carol.expectNone(t, "Chat") // not in the room

	ctx, cancel := deadline()
	defer cancel()
	if _, err := bob.JoinRoom(ctx, "staff"); err == nil || !strings.Contains(err.Error(), "staff only") {
		t.Fatalf("bob joined #staff: %v", err)
	}
	alice.join(t, "staff") // alice is staff
}

func TestAsk(t *testing.T) {
	ws, url := startServer(t)
	alice := connect(t, url, "alice")
	bob := connect(t, url, "bob")
	alice.join(t, "staff")

	ctx, cancel := deadline()
	defer cancel()

	reply, err := bob.Ask(ctx, "Who", nil)
	if err != nil {
		t.Fatal(err)
	}
	names, err := reply.As[[]string]()
	if err != nil || !slices.Equal(names, []string{"alice", "bob"}) {
		t.Fatalf("who: got %v (%v), want [alice bob]", names, err)
	}

	// Registered known errors come back as the same values.
	if _, err := bob.Ask(ctx, "Who", []byte("staff")); !errors.Is(err, errNotInRoom) {
		t.Fatalf("who staff: expected errNotInRoom, got %v", err)
	}
	if _, err := bob.Ask(ctx, "Who", []byte("nowhere")); !errors.Is(err, errEmptyRoom) {
		t.Fatalf("who nowhere: expected errEmptyRoom, got %v", err)
	}

	// The server asks one client by its ID.
	reply, err = ws.Ask(ctx, neffos.Message{To: "bob", Namespace: namespace, Event: "Ping"})
	if err != nil || string(reply.Body) != "pong" {
		t.Fatalf("ping bob: got %q (%v), want pong", reply.Body, err)
	}
}

func TestCloseCodes(t *testing.T) {
	ws, url := startServer(t)
	alice := connect(t, url, "alice")
	bob := connect(t, url, "bob")

	// SendObject hands a line over the size limit to the socket without an error;
	// the server then closes the connection with 1009.
	spam := chatMessage{Text: strings.Repeat("a", 2*maxMessageSize)}
	if err := bob.SendObject("Chat", spam); err != nil {
		t.Fatalf("bob: send: %v", err)
	}
	if code := neffos.CloseStatus(bob.expectClose(t)); code != neffos.CloseMessageTooBig {
		t.Fatalf("bob: expected close status %d, got %d", neffos.CloseMessageTooBig, code)
	}

	// The operator's kick reaches the client with its code and reason.
	ws.GetConnections()["alice"].Terminate(closeKicked, "kicked by operator")
	ce, ok := errors.AsType[neffos.CloseError](alice.expectClose(t))
	if !ok || ce.Code != closeKicked || ce.Reason != "kicked by operator" {
		t.Fatalf("alice: expected [%d] kicked by operator, got %v", closeKicked, ce)
	}
}

func TestAuthentication(t *testing.T) {
	_, url := startServer(t)

	if _, err := dial(t, url, "eve-token"); err == nil {
		t.Fatal("an unknown token was accepted")
	}
	if _, err := dial(t, url, "mallory-token"); err == nil || !strings.Contains(err.Error(), "banned") {
		t.Fatalf("mallory: expected a banned error, got %v", err)
	}

	// Behind the middleware, an unknown token never reaches the upgrade.
	ws := newServer()
	ts := httptest.NewServer(authenticate(ws))
	t.Cleanup(ts.Close)
	t.Cleanup(ws.Close)

	if _, err := dial(t, "ws"+strings.TrimPrefix(ts.URL, "http"), "eve-token"); err == nil {
		t.Fatal("the middleware let an unknown token through")
	}
	if n := ws.GetTotalConnections(); n != 0 {
		t.Fatalf("expected no websocket behind the middleware, got %d", n)
	}
}
