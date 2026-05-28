package neffos

import (
	"context"
	"errors"
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
