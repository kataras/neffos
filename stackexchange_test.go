package neffos

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockStackExchange is a minimal StackExchange used to validate the wrapper
// chain and Server interactions without requiring a real broker.
type mockStackExchange struct {
	name          string
	connectErr    error
	publishOK     bool
	askErr        error
	askReply      Message
	notifyAskErr  error
	closeErr      error
	closed        bool
	onConnectN    int
	onDisconnectN int
	publishN      int
	subscribeN    int
	unsubscribeN  int
	askN          int
	notifyAskN    int
	askMsgs       []Message
	askTokens     []string
}

func (m *mockStackExchange) OnConnect(c *Conn) error { m.onConnectN++; return m.connectErr }
func (m *mockStackExchange) OnDisconnect(c *Conn)    { m.onDisconnectN++ }
func (m *mockStackExchange) Publish(msgs []Message) bool {
	m.publishN++
	return m.publishOK
}
func (m *mockStackExchange) Subscribe(c *Conn, namespace string)   { m.subscribeN++ }
func (m *mockStackExchange) Unsubscribe(c *Conn, namespace string) { m.unsubscribeN++ }
func (m *mockStackExchange) Ask(ctx context.Context, msg Message, token string) (Message, error) {
	m.askN++
	m.askMsgs = append(m.askMsgs, msg)
	m.askTokens = append(m.askTokens, token)
	return m.askReply, m.askErr
}
func (m *mockStackExchange) NotifyAsk(msg Message, token string) error {
	m.notifyAskN++
	return m.notifyAskErr
}
func (m *mockStackExchange) Close() error { m.closed = true; return m.closeErr }

var _ StackExchange = (*mockStackExchange)(nil)
var _ StackExchangeCloser = (*mockStackExchange)(nil)

func TestStackExchangeWrapperPublishShortCircuit(t *testing.T) {
	parent := &mockStackExchange{name: "parent", publishOK: false}
	current := &mockStackExchange{name: "current", publishOK: true}

	w := wrapStackExchanges(parent, current)
	if w.Publish([]Message{{}}) {
		t.Fatal("expected false when parent fails")
	}
	// the wrapper Publish calls both regardless (so a partial broadcast through
	// the second exchange still happens) but reports failure on any failure.
	if parent.publishN != 1 || current.publishN != 1 {
		t.Fatalf("expected both to be called, got parent=%d current=%d", parent.publishN, current.publishN)
	}
}

func TestStackExchangeWrapperAskFallthrough(t *testing.T) {
	parent := &mockStackExchange{name: "parent", askErr: errors.New("parent down")}
	wantReply := Message{Event: "ok"}
	current := &mockStackExchange{name: "current", askReply: wantReply}

	w := wrapStackExchanges(parent, current)
	got, err := w.Ask(context.Background(), Message{}, "token")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got.Event != wantReply.Event {
		t.Fatalf("expected reply from current, got %+v", got)
	}
	if parent.askN != 1 || current.askN != 1 {
		t.Fatalf("expected both Ask calls, got parent=%d current=%d", parent.askN, current.askN)
	}
}

func TestStackExchangeWrapperAskBothFail(t *testing.T) {
	parent := &mockStackExchange{name: "parent", askErr: errors.New("parent down")}
	current := &mockStackExchange{name: "current", askErr: errors.New("current down")}

	w := wrapStackExchanges(parent, current)
	_, err := w.Ask(context.Background(), Message{}, "token")
	if err == nil {
		t.Fatal("expected error when both fail")
	}
}

func TestStackExchangeWrapperClose(t *testing.T) {
	parent := &mockStackExchange{}
	current := &mockStackExchange{}

	w := wrapStackExchanges(parent, current).(*stackExchangeWrapper)
	if err := w.Close(); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if !parent.closed || !current.closed {
		t.Fatal("expected both wrapped exchanges to be closed")
	}
}

func TestStackExchangeWrapperCloseFirstError(t *testing.T) {
	want := errors.New("boom")
	parent := &mockStackExchange{closeErr: want}
	current := &mockStackExchange{}

	w := wrapStackExchanges(parent, current).(*stackExchangeWrapper)
	if err := w.Close(); !errors.Is(err, want) {
		t.Fatalf("expected first error to propagate, got %v", err)
	}
	if !current.closed {
		t.Fatal("expected current to still be closed even after parent error")
	}
}

func TestStackExchangeWrapperAskPassesOriginalMessage(t *testing.T) {
	parent := &mockStackExchange{name: "parent", askErr: errors.New("parent down")}
	current := &mockStackExchange{name: "current", askReply: Message{Event: "reply"}}

	w := wrapStackExchanges(parent, current)
	orig := Message{Namespace: "default", Event: "question", Body: []byte("body")}
	if _, err := w.Ask(context.Background(), orig, "token"); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}

	if len(current.askMsgs) != 1 {
		t.Fatalf("expected one Ask on current, got %d", len(current.askMsgs))
	}
	got := current.askMsgs[0]
	if got.Namespace != orig.Namespace || got.Event != orig.Event || string(got.Body) != string(orig.Body) {
		t.Fatalf("expected current to be asked with the original message %+v, got %+v", orig, got)
	}
	if current.askTokens[0] != "token" {
		t.Fatalf("expected the same token, got %q", current.askTokens[0])
	}
}

func TestServerAskStackExchangeTokens(t *testing.T) {
	exc := &mockStackExchange{askReply: Message{Event: "reply"}}
	s := New(nil, Namespaces{"default": Events{}})
	t.Cleanup(s.Close)
	if err := s.UseStackExchange(exc); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Ask(context.Background(), Message{Namespace: "default", Event: "question"}); err != nil {
		t.Fatalf("Server.Ask: %v", err)
	}
	if exc.askN != 1 {
		t.Fatalf("expected one exchange Ask, got %d", exc.askN)
	}

	token := exc.askTokens[0]
	if !strings.HasSuffix(token, "-"+s.uuid[:8]) {
		t.Fatalf("expected the token to end with the server uuid prefix %q, got %q", s.uuid[:8], token)
	}
	if strings.ContainsRune(token, waitComesFromStackExchange) || token[0] == waitComesFromClientPrefix {
		t.Fatalf("expected the exchange to get the clean token, got %q", token)
	}

	marked := token[:1] + "!" + token[1:]
	if wire := serializeMessage(exc.askMsgs[0]); !strings.HasPrefix(string(wire), marked+";") {
		t.Fatalf("expected the wire frame to carry the marked token %q, got %q", marked, wire)
	}
}

// TestServerAskAfterCloseWithExchange pins the Close doc: after Close,
// Server.Ask returns ErrServerClosed even when a StackExchange is in use,
// and the exchange is not asked.
func TestServerAskAfterCloseWithExchange(t *testing.T) {
	exc := &mockStackExchange{askReply: Message{Event: "reply"}}
	s := New(nil, Namespaces{"default": Events{}})
	if err := s.UseStackExchange(exc); err != nil {
		t.Fatal(err)
	}
	s.Close()

	_, err := s.Ask(context.Background(), Message{Namespace: "default", Event: "question"})
	if !errors.Is(err, ErrServerClosed) {
		t.Fatalf("expected ErrServerClosed, got %v", err)
	}
	if exc.askN != 0 {
		t.Fatalf("expected no exchange Ask after Close, got %d", exc.askN)
	}
}

func TestStackExchangeUnmatchedReplyFallback(t *testing.T) {
	exc := &mockStackExchange{}
	s := New(nil, Namespaces{"default": Events{}})
	t.Cleanup(s.Close)
	if err := s.UseStackExchange(exc); err != nil {
		t.Fatal(err)
	}

	c := newConn(newFakeSocket(), Namespaces{"default": Events{}})
	c.server = s

	reply := func(wait string) Message {
		return c.DeserializeMessage(TextMessage, serializeMessage(Message{wait: wait, Namespace: "default", Event: "question"}))
	}

	// an unmatched reply without the client prefix goes to the exchange by token.
	unmatched := "k2x9-1f-" + s.uuid[:8]
	if err := c.handleMessage(reply(unmatched)); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if exc.notifyAskN != 1 {
		t.Fatalf("expected the unmatched reply to reach NotifyAsk, got %d calls", exc.notifyAskN)
	}

	// a reply matched by the connection's own waiter does not.
	connWait := "k2xa-20"
	connCh := make(chan Message, 1)
	c.waitingMessages[connWait] = connCh
	if err := c.handleMessage(reply(connWait)); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(connCh) != 1 {
		t.Fatal("expected the matched reply to reach the connection waiter")
	}

	// nor does a reply matched by a local Server.Ask waiter.
	serverWait := "k2xb-21-" + s.uuid[:8]
	serverCh := make(chan Message, 1)
	s.waitingMessagesMutex.Lock()
	s.waitingMessages[serverWait] = serverCh
	s.waitingMessagesMutex.Unlock()
	if err := c.handleMessage(reply(serverWait)); err != nil {
		t.Fatalf("handleMessage: %v", err)
	}
	if len(serverCh) != 1 {
		t.Fatal("expected the matched reply to reach the server waiter")
	}

	if exc.notifyAskN != 1 {
		t.Fatalf("expected matched replies to skip NotifyAsk, got %d calls", exc.notifyAskN)
	}
}
