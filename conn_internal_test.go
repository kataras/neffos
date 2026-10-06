package neffos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAskReturnsOnClose(t *testing.T) {
	c, s := newFakeClientConn(t)
	ns := connectFake(t, c, s, fakeNamespace)

	errCh := make(chan error, 1)
	go func() {
		_, err := ns.Ask(context.Background(), "question", nil)
		errCh <- err
	}()

	s.next(t) // the question is on the wire, Ask is waiting for the reply.
	c.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrWrite) {
			t.Fatalf("expected errors.Is(err, ErrWrite), got %v", err)
		}
		if !IsCloseError(err) {
			t.Fatalf("expected IsCloseError(err), got %v", err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("Ask did not return after Close")
	}

	// a closed connection fails fast with the same error.
	_, err := ns.Ask(context.Background(), "question", nil)
	if !errors.Is(err, ErrWrite) || !IsCloseError(err) {
		t.Fatalf("expected the closed error on a closed conn, got %v", err)
	}
}

func TestAskCancelledContextDoesNotWrite(t *testing.T) {
	c, s := newFakeClientConn(t)
	ns := connectFake(t, c, s, fakeNamespace)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	before := s.writeCalls.Load()
	_, err := ns.Ask(ctx, "question", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if after := s.writeCalls.Load(); after != before {
		t.Fatalf("expected no write for a cancelled context, got %d", after-before)
	}
}

func TestAskInsideHandlerSkipsUnrelatedMessage(t *testing.T) {
	type askResult struct {
		msg Message
		err error
	}
	askCh := make(chan askResult, 1)
	otherCh := make(chan string, 4)

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		"trigger": func(ns *NSConn, msg Message) error {
			ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
			defer cancel()
			reply, err := ns.Ask(ctx, "question", nil)
			askCh <- askResult{reply, err}
			return nil
		},
		"other": func(ns *NSConn, msg Message) error {
			otherCh <- string(msg.Body)
			return nil
		},
	}})
	connectFake(t, c, s, fakeNamespace)

	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "trigger"}))

	question := s.nextMessage(t)
	if question.Event != "question" || question.wait == "" {
		t.Fatalf("expected the in-handler question with a wait token, got %+v", question)
	}

	// an unrelated event arrives before the reply.
	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "other", Body: []byte("unrelated")}))
	s.push(t, serializeMessage(Message{wait: question.wait, Namespace: fakeNamespace, Event: "question", Body: []byte("answer")}))

	select {
	case r := <-askCh:
		if r.err != nil {
			t.Fatalf("in-handler Ask failed: %v", r.err)
		}
		if string(r.msg.Body) != "answer" {
			t.Fatalf("expected the reply body %q, got %q (event %q)", "answer", r.msg.Body, r.msg.Event)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("in-handler Ask did not return")
	}

	select {
	case body := <-otherCh:
		if body != "unrelated" {
			t.Fatalf("expected the unrelated event body, got %q", body)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("the unrelated event was not dispatched")
	}

	select {
	case body := <-otherCh:
		t.Fatalf("the unrelated event was dispatched twice (second body %q)", body)
	default:
	}

	if n := s.maxConcurrent.Load(); n != 1 {
		t.Fatalf("expected at most one concurrent ReadData, got %d", n)
	}
}

// An Ask made from another goroutine while the reader runs a callback must not
// start a second concurrent read of the socket, and must still get its reply.
func TestAskOutsideHandlerKeepsSingleReader(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		"slow": func(ns *NSConn, msg Message) error {
			close(entered)
			<-release
			return nil
		},
	}})
	ns := connectFake(t, c, s, fakeNamespace)

	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "slow"}))
	waitDone(t, entered, "the slow callback")

	type askResult struct {
		msg Message
		err error
	}
	askCh := make(chan askResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
		defer cancel()
		reply, err := ns.Ask(ctx, "question", nil)
		askCh <- askResult{reply, err}
	}()

	question := s.nextMessage(t)
	close(release)

	// give the reader loop the chance to go back to the socket; a second
	// concurrent read shows up here on the broken design.
	for deadline := time.Now().Add(100 * time.Millisecond); time.Now().Before(deadline); {
		if s.maxConcurrent.Load() > 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	s.push(t, serializeMessage(Message{wait: question.wait, Namespace: fakeNamespace, Event: "question", Body: []byte("answer")}))

	select {
	case r := <-askCh:
		if r.err != nil || string(r.msg.Body) != "answer" {
			t.Fatalf("expected the reply %q, got %q (err %v)", "answer", r.msg.Body, r.err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("Ask did not return")
	}

	if n := s.maxConcurrent.Load(); n != 1 {
		t.Fatalf("expected at most one concurrent ReadData, got %d", n)
	}
}

func TestCloseErrorLiteralNoPanic(t *testing.T) {
	var got string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("CloseError{Code: 1008}.Error() panicked: %v", r)
			}
		}()
		got = CloseError{Code: 1008}.Error()
	}()
	if want := "[1008] "; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if got, want := (CloseError{Code: 1008, Reason: "policy"}).Error(), "[1008] policy"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if got, want := (CloseError{Code: -1, Reason: "ignored", error: ErrWrite}).Error(), "[-1] "+ErrWrite.Error(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
	if err := error(CloseError{Code: -1, error: ErrWrite}); !errors.Is(err, ErrWrite) {
		t.Fatal("expected errors.Is(CloseError{error: ErrWrite}, ErrWrite)")
	}
}

func TestDisconnectAllCallbackReadsNamespace(t *testing.T) {
	called := make(chan struct{})
	var once sync.Once

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		OnNamespaceDisconnect: func(ns *NSConn, msg Message) error {
			// reads the connection's namespaces from inside the callback.
			_ = ns.Conn.Namespace(fakeNamespace)
			once.Do(func() { close(called) })
			return nil
		},
	}})
	connectFake(t, c, s, fakeNamespace)

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
		defer cancel()
		errCh <- c.DisconnectAll(ctx)
	}()

	s.answerNext(t)
	waitDone(t, called, "the OnNamespaceDisconnect callback")

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("DisconnectAll: %v", err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("DisconnectAll did not return")
	}

	if c.Namespace(fakeNamespace) != nil {
		t.Fatal("expected the namespace to be disconnected")
	}
}

func TestLeaveAllCallbackReadsRooms(t *testing.T) {
	called := make(chan struct{})
	var once sync.Once

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		OnRoomLeft: func(ns *NSConn, msg Message) error {
			// reads the namespace's rooms from inside the callback.
			_ = ns.Rooms()
			_ = ns.Room(msg.Room)
			once.Do(func() { close(called) })
			return nil
		},
	}})
	ns := connectFake(t, c, s, fakeNamespace)
	joinFake(t, ns, s, "room1")

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
		defer cancel()
		errCh <- ns.LeaveAll(ctx)
	}()

	s.answerNext(t)
	waitDone(t, called, "the OnRoomLeft callback")

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("LeaveAll: %v", err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("LeaveAll did not return")
	}

	if len(ns.Rooms()) != 0 {
		t.Fatalf("expected no rooms after LeaveAll, got %d", len(ns.Rooms()))
	}
}

func TestCloseCallbacksNotUnderLock(t *testing.T) {
	var disconnects, lefts atomic.Int32

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		OnNamespaceDisconnect: func(ns *NSConn, msg Message) error {
			_ = ns.Conn.Namespace(fakeNamespace)
			disconnects.Add(1)
			return nil
		},
		OnRoomLeft: func(ns *NSConn, msg Message) error {
			_ = ns.Rooms()
			_ = ns.Conn.Namespace(fakeNamespace)
			lefts.Add(1)
			return nil
		},
	}})
	ns := connectFake(t, c, s, fakeNamespace)
	joinFake(t, ns, s, "room1")

	closed := make(chan struct{})
	go func() {
		c.Close()
		close(closed)
	}()
	waitDone(t, closed, "Close")

	if n := disconnects.Load(); n != 1 {
		t.Fatalf("expected one OnNamespaceDisconnect, got %d", n)
	}
	if n := lefts.Load(); n != 1 {
		t.Fatalf("expected one OnRoomLeft, got %d", n)
	}
}

func TestIncrementConcurrent(t *testing.T) {
	c := newConn(newFakeSocket(), Namespaces{})

	const (
		workers = 50
		perG    = 200
	)

	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range perG {
				c.Increment("n")
				c.Increment("m")
				c.Decrement("m")
			}
		})
	}
	wg.Wait()

	if got, _ := c.Value[int]("n"); got != workers*perG {
		t.Fatalf("expected %d increments, got %v", workers*perG, got)
	}
	if got, _ := c.Value[int]("m"); got != 0 {
		t.Fatalf("expected balanced increments and decrements to give 0, got %v", got)
	}
}

func TestConnValueTyped(t *testing.T) {
	c := newConn(newFakeSocket(), Namespaces{})

	if _, ok := c.Value[string]("missing"); ok {
		t.Fatal("expected a missing key to report false")
	}

	c.Set("user", "makis")
	got, ok := c.Value[string]("user")
	if !ok || got != "makis" {
		t.Fatalf("expected the stored string, got %q (%v)", got, ok)
	}

	if n, ok := c.Value[int]("user"); ok || n != 0 {
		t.Fatalf("expected a wrong type to report the zero value and false, got %d (%v)", n, ok)
	}

	if v, ok := c.Value[any]("user"); !ok || v != "makis" {
		t.Fatalf("expected Value[any] to behave like the old Get, got %v (%v)", v, ok)
	}
}

func TestWaitConnectReturnsOnClose(t *testing.T) {
	c, s := newFakeClientConn(t)

	// wakes up when the namespace connects.
	nsCh := make(chan *NSConn, 1)
	go func() {
		ns, _ := c.WaitConnect(context.Background(), fakeNamespace)
		nsCh <- ns
	}()
	connectFake(t, c, s, fakeNamespace)
	select {
	case ns := <-nsCh:
		if ns == nil || ns.namespace != fakeNamespace {
			t.Fatalf("expected the connected namespace, got %v", ns)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("WaitConnect did not return after the namespace connected")
	}

	// returns when the connection closes, even without a deadline.
	errCh := make(chan error, 1)
	go func() {
		_, err := c.WaitConnect(context.Background(), "never-connected")
		errCh <- err
	}()
	c.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrWrite) || !IsCloseError(err) {
			t.Fatalf("expected the closed-connection error, got %v", err)
		}
	case <-time.After(fakeTimeout):
		t.Fatal("WaitConnect did not return after Close")
	}
}

func TestClientSideBroadcastNoPanic(t *testing.T) {
	c, s := newFakeClientConn(t)
	ns := connectFake(t, c, s, fakeNamespace)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("client-side Broadcast panicked: %v", r)
		}
	}()

	before := s.writeCalls.Load()
	ns.Broadcast(Message{Namespace: fakeNamespace, Event: "e"})
	ns.BroadcastOthers(Message{Namespace: fakeNamespace, Event: "e"})
	if after := s.writeCalls.Load(); after != before {
		t.Fatalf("expected client-side Broadcast to write nothing, got %d writes", after-before)
	}
}

// An outside Ask that took the driver path receives an event frame whose
// callback Asks again from outside the driver path, while the reader loop is
// blocked waiting for the driver's read. A read must still be in flight, so
// both asks get their replies.
func TestAskOutsideDriverEventCallbackAsks(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	type askResult struct {
		msg Message
		err error
	}
	innerCh := make(chan askResult, 1)

	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		"slow": func(ns *NSConn, msg Message) error {
			close(entered)
			<-release
			return nil
		},
		"asker": func(ns *NSConn, msg Message) error {
			ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
			defer cancel()
			reply, err := ns.Ask(ctx, "inner", nil)
			innerCh <- askResult{reply, err}
			return nil
		},
	}})
	ns := connectFake(t, c, s, fakeNamespace)

	// (a) the reader goroutine is inside the "slow" callback.
	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "slow"}))
	waitDone(t, entered, "the slow callback")

	// an outside Ask sees isInsideHandler==1 and starts the driver read R1.
	outerCh := make(chan askResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fakeTimeout)
		defer cancel()
		reply, err := ns.Ask(ctx, "outer", nil)
		outerCh <- askResult{reply, err}
	}()
	outer := s.nextMessage(t)

	// (b) the slow callback returns; the reader loop clears isInsideHandler
	// and blocks in readNext on stolen behind the outside Ask.
	close(release)
	for deadline := time.Now().Add(fakeTimeout); c.isInsideHandler.Load() != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the reader loop did not leave the slow callback")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the reader loop park on stolen.

	// (c) R1 delivers an event whose callback asks from outside the driver path.
	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "asker"}))
	inner := s.nextMessage(t)
	if inner.Event != "inner" {
		t.Fatalf("expected the inner question, got %+v", inner)
	}

	// (d) someone must still be reading the socket for both replies to land.
	s.push(t, serializeMessage(Message{wait: inner.wait, Namespace: fakeNamespace, Event: "inner", Body: []byte("inner answer")}))
	s.push(t, serializeMessage(Message{wait: outer.wait, Namespace: fakeNamespace, Event: "outer", Body: []byte("outer answer")}))

	for _, tc := range []struct {
		name string
		ch   chan askResult
		want string
	}{{"inner", innerCh, "inner answer"}, {"outer", outerCh, "outer answer"}} {
		select {
		case r := <-tc.ch:
			if r.err != nil || string(r.msg.Body) != tc.want {
				t.Fatalf("%s Ask: expected %q, got %q (err %v)", tc.name, tc.want, r.msg.Body, r.err)
			}
		case <-time.After(fakeTimeout + time.Second):
			t.Fatalf("%s Ask did not return", tc.name)
		}
	}

	if n := s.maxConcurrent.Load(); n != 1 {
		t.Fatalf("expected at most one concurrent ReadData, got %d", n)
	}
}

func TestCloseStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, -1},
		{"plain error", errors.New("boom"), -1},
		{"EOF", io.EOF, -1},
		{"close error", CloseError{Code: CloseGoingAway}, CloseGoingAway},
		{"wrapped close error", fmt.Errorf("read: %w", CloseError{Code: CloseMessageTooBig, Reason: "big"}), CloseMessageTooBig},
		{"closed connection", CloseError{Code: -1, error: ErrWrite}, -1},
	}

	for _, tt := range tests {
		if got := CloseStatus(tt.err); got != tt.want {
			t.Errorf("%s: expected %d, got %d", tt.name, tt.want, got)
		}
	}

	if CloseNormalClosure != 1000 || CloseGoingAway != 1001 || ClosePolicyViolation != 1008 ||
		CloseMessageTooBig != 1009 || CloseTLSHandshake != 1015 {
		t.Fatal("close codes do not match RFC 6455")
	}
}

func TestTerminateUsesSocketCloser(t *testing.T) {
	c, s := newFakeClientConn(t)
	c.Terminate(ClosePolicyViolation, "policy")
	waitDone(t, s.closed, "socket close")

	if n := s.closerCalls.Load(); n != 1 {
		t.Fatalf("expected one SocketCloser.Close call, got %d", n)
	}
	if code, reason := s.lastClose(); code != ClosePolicyViolation || reason != "policy" {
		t.Fatalf("expected close frame [1008] policy, got [%d] %s", code, reason)
	}
	if n := s.netCloseCalls.Load(); n != 0 {
		t.Fatalf("expected NetConn().Close not to be called, got %d calls", n)
	}

	// Close is Terminate(CloseNormalClosure, "").
	c2, s2 := newFakeClientConn(t)
	c2.Close()
	if code, reason := s2.lastClose(); code != CloseNormalClosure || reason != "" {
		t.Fatalf("Close: expected close frame [1000], got [%d] %q", code, reason)
	}

	// The reason is cut to 123 bytes on a UTF-8 boundary.
	c3, s3 := newFakeClientConn(t)
	c3.Terminate(CloseGoingAway, strings.Repeat("é", 100))
	_, reason := s3.lastClose()
	if len(reason) > 123 || !utf8.ValidString(reason) || reason == "" {
		t.Fatalf("expected a valid UTF-8 reason of at most 123 bytes, got %d bytes (valid=%v)", len(reason), utf8.ValidString(reason))
	}
}

func TestCloseFallsBackToNetConn(t *testing.T) {
	s := newFakeSocket()
	c := newFakeClientConnOn(t, fakeSocketBare{s}, s)

	c.Terminate(CloseGoingAway, "bye")
	waitDone(t, s.closed, "socket close")

	if n := s.netCloseCalls.Load(); n != 1 {
		t.Fatalf("expected one NetConn().Close call, got %d", n)
	}
	if n := s.closerCalls.Load(); n != 0 {
		t.Fatalf("expected no SocketCloser call through a bare socket, got %d", n)
	}
	if got := CloseStatus(c.Err()); got != CloseGoingAway {
		t.Fatalf("expected Err() with code %d, got %d (%v)", CloseGoingAway, got, c.Err())
	}
}

func TestConnErrAfterTerminate(t *testing.T) {
	c, _ := newFakeClientConn(t)
	if err := c.Err(); err != nil {
		t.Fatalf("expected a nil Err() while open, got %v", err)
	}

	c.Terminate(ClosePolicyViolation, "policy")

	err := c.Err()
	if got := CloseStatus(err); got != ClosePolicyViolation {
		t.Fatalf("expected Err() code %d, got %d (%v)", ClosePolicyViolation, got, err)
	}
	if ce, _ := errors.AsType[CloseError](err); ce.Reason != "policy" {
		t.Fatalf("expected Err() reason %q, got %q", "policy", ce.Reason)
	}

	_, askErr := c.Ask(context.Background(), Message{Namespace: fakeNamespace, Event: "x"})
	if got := CloseStatus(askErr); got != ClosePolicyViolation {
		t.Fatalf("expected Ask error code %d, got %d (%v)", ClosePolicyViolation, got, askErr)
	}
	if !errors.Is(askErr, ErrWrite) || !IsCloseError(askErr) {
		t.Fatalf("expected Ask error to be ErrWrite and a close error, got %v", askErr)
	}
}

func TestConnErrAfterRemoteClose(t *testing.T) {
	c, s := newFakeClientConn(t)
	s.remoteClose(t, CloseError{Code: CloseGoingAway, Reason: "restart"})
	waitDone(t, s.closed, "socket close")

	if got := CloseStatus(c.Err()); got != CloseGoingAway {
		t.Fatalf("expected Err() code %d, got %d (%v)", CloseGoingAway, got, c.Err())
	}
	if n := s.closerCalls.Load(); n != 0 {
		t.Fatalf("expected no close frame back to a remote close, got %d", n)
	}
	if n := s.netCloseCalls.Load(); n != 1 {
		t.Fatalf("expected the transport to be closed once, got %d", n)
	}

	c2, s2 := newFakeClientConn(t)
	s2.remoteClose(t, ErrMessageTooBig)
	waitDone(t, s2.closed, "socket close")

	if err := c2.Err(); !errors.Is(err, ErrMessageTooBig) {
		t.Fatalf("expected Err() to be ErrMessageTooBig, got %v", err)
	}
	if n := s2.closerCalls.Load(); n != 0 {
		t.Fatalf("expected no close frame after ErrMessageTooBig, got %d", n)
	}
}

func TestHandlerCloseErrorTerminates(t *testing.T) {
	c, s := newFakeClientConn(t, Namespaces{fakeNamespace: Events{
		"kick": func(*NSConn, Message) error {
			return CloseError{Code: ClosePolicyViolation, Reason: "kicked"}
		},
	}})
	connectFake(t, c, s, fakeNamespace)

	s.push(t, serializeMessage(Message{Namespace: fakeNamespace, Event: "kick"}))
	waitDone(t, s.closed, "socket close")

	if code, reason := s.lastClose(); code != ClosePolicyViolation || reason != "kicked" {
		t.Fatalf("expected close frame [1008] kicked, got [%d] %s", code, reason)
	}
	if got := CloseStatus(c.Err()); got != ClosePolicyViolation {
		t.Fatalf("expected Err() code %d, got %d", ClosePolicyViolation, got)
	}
}

// waitFor polls cond until it holds or fakeTimeout passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(fakeTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in time", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHeartbeatPingsAtInterval(t *testing.T) {
	const interval = 10 * time.Millisecond
	c, s := newFakeClientConn(t, WithTimeout{PingInterval: interval, Namespaces: Namespaces{fakeNamespace: Events{}}})

	waitFor(t, "three pings", func() bool { return s.pingCalls.Load() >= 3 })
	if got := s.lastPingTimeout(); got != interval {
		t.Fatalf("expected ping timeout %s, got %s", interval, got)
	}
	if c.IsClosed() {
		t.Fatalf("expected the connection to stay open, got %v", c.Err())
	}

	c.Close()
	time.Sleep(2 * interval)
	n := s.pingCalls.Load()
	time.Sleep(5 * interval)
	if got := s.pingCalls.Load(); got != n {
		t.Fatalf("expected no pings after Close, got %d more", got-n)
	}
}

func TestHeartbeatPongTimeoutCloses(t *testing.T) {
	s := newFakeSocket()
	s.setPingErr(os.ErrDeadlineExceeded)
	c := newFakeClientConnOn(t, s, s, WithTimeout{PingInterval: 10 * time.Millisecond, Namespaces: Namespaces{fakeNamespace: Events{}}})

	waitDone(t, s.closed, "socket close")

	err := c.Err()
	if !errors.Is(err, os.ErrDeadlineExceeded) || !IsTimeoutError(err) {
		t.Fatalf("expected a timeout error from Err(), got %v", err)
	}
	if !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("expected the error to name the heartbeat, got %q", err)
	}
	if n := s.closerCalls.Load(); n != 0 {
		t.Fatalf("expected no close frame on a dead connection, got %d", n)
	}
}

func TestHeartbeatSkippedWithoutPinger(t *testing.T) {
	// a socket without SocketPinger: PingInterval does nothing.
	s := newFakeSocket()
	c := newFakeClientConnOn(t, fakeSocketBare{s}, s, WithTimeout{PingInterval: 5 * time.Millisecond, Namespaces: Namespaces{fakeNamespace: Events{}}})

	// a pinger without PingInterval: no heartbeat either.
	c2, s2 := newFakeClientConn(t)

	time.Sleep(50 * time.Millisecond)
	if n := s.pingCalls.Load() + s2.pingCalls.Load(); n != 0 {
		t.Fatalf("expected no pings, got %d", n)
	}
	if c.IsClosed() || c2.IsClosed() {
		t.Fatal("expected both connections to stay open")
	}
}

func TestApplySettingsSetsReadLimit(t *testing.T) {
	_, s := newFakeClientConn(t, WithTimeout{MaxMessageSize: 1024, Namespaces: Namespaces{fakeNamespace: Events{}}})
	if n := s.readLimitCalls.Load(); n != 1 {
		t.Fatalf("expected one SetReadLimit call, got %d", n)
	}
	if got := s.readLimit.Load(); got != 1024 {
		t.Fatalf("expected read limit 1024, got %d", got)
	}

	_, s2 := newFakeClientConn(t)
	if n := s2.readLimitCalls.Load(); n != 0 {
		t.Fatalf("expected no SetReadLimit call without MaxMessageSize, got %d", n)
	}
}

func TestSendErrors(t *testing.T) {
	c, s := newFakeClientConn(t)

	if err := c.Send(Message{Namespace: fakeNamespace, Event: "x"}); !errors.Is(err, ErrBadNamespace) {
		t.Fatalf("not connected: expected ErrBadNamespace, got %v", err)
	}
	var nilNS *NSConn
	if err := nilNS.Send("x", nil); !errors.Is(err, ErrBadNamespace) {
		t.Fatalf("nil NSConn: expected ErrBadNamespace, got %v", err)
	}

	ns := connectFake(t, c, s, fakeNamespace)
	if err := ns.Send("x", []byte("hi")); err != nil {
		t.Fatalf("expected a nil error, got %v", err)
	}
	if msg := s.nextMessage(t); msg.Event != "x" || string(msg.Body) != "hi" {
		t.Fatalf("expected event x with body hi, got %q %q", msg.Event, msg.Body)
	}

	if err := c.Send(Message{Namespace: fakeNamespace, Room: "r", Event: "x"}); !errors.Is(err, ErrBadRoom) {
		t.Fatalf("not joined: expected ErrBadRoom, got %v", err)
	}
	room := joinFake(t, ns, s, "r")
	if err := room.Send("x", nil); err != nil {
		t.Fatalf("room: expected a nil error, got %v", err)
	}
	if msg := s.nextMessage(t); msg.Room != "r" {
		t.Fatalf("expected room r, got %q", msg.Room)
	}

	if err := c.Send(Message{Namespace: fakeNamespace, Event: "x", FromExplicit: c.ID()}); !errors.Is(err, errExcluded) {
		t.Fatalf("excluded: expected errExcluded, got %v", err)
	}

	boom := errors.New("boom")
	s.setWriteErr(boom)
	if err := ns.Send("x", nil); !errors.Is(err, boom) {
		t.Fatalf("socket error: expected %v, got %v", boom, err)
	}
	if ns.Emit("x", nil) {
		t.Fatal("socket error: expected Emit to return false")
	}
	s.setWriteErr(nil)

	c.Close()
	for name, err := range map[string]error{
		"conn": c.Send(Message{Namespace: fakeNamespace, Event: "x"}),
		"ns":   ns.Send("x", nil),
		"room": room.Send("x", nil),
	} {
		if !errors.Is(err, ErrWrite) || !IsCloseError(err) {
			t.Fatalf("%s after Close: expected the closed-connection error, got %v", name, err)
		}
	}
}

func TestMessageTypeString(t *testing.T) {
	for typ, want := range map[MessageType]string{
		TextMessage:    "text",
		BinaryMessage:  "binary",
		MessageType(0): "unknown",
		MessageType(9): "unknown",
	} {
		if got := typ.String(); got != want {
			t.Errorf("MessageType(%d): expected %q, got %q", uint8(typ), want, got)
		}
	}

	if got := fmt.Sprintf("%T", TextMessage); got != "neffos.MessageType" {
		t.Fatalf("expected TextMessage to be a typed MessageType, got %s", got)
	}
}

func TestIsCloseErrorNetErrClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	_, acceptErr := ln.Accept()

	for name, err := range map[string]error{
		"net.ErrClosed":   net.ErrClosed,
		"wrapped":         fmt.Errorf("read: %w", net.ErrClosed),
		"closed listener": acceptErr,
		"closed conn":     closedConnReadErr(t),
	} {
		if !IsCloseError(err) {
			t.Errorf("%s: expected IsCloseError(%v) to be true", name, err)
		}
	}
}

// closedConnReadErr returns the error of a Read on a TCP connection closed locally.
func closedConnReadErr(t *testing.T) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	_, err = conn.Read(make([]byte, 1))
	return err
}

func TestIsTimeoutErrorContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	for name, err := range map[string]error{
		"context.DeadlineExceeded": context.DeadlineExceeded,
		"ctx.Err()":                ctx.Err(),
		"wrapped":                  fmt.Errorf("ask: %w", context.DeadlineExceeded),
		"os.ErrDeadlineExceeded":   os.ErrDeadlineExceeded,
	} {
		if !IsTimeoutError(err) {
			t.Errorf("%s: expected IsTimeoutError to be true", name)
		}
	}

	if IsTimeoutError(errors.New("boom")) || IsTimeoutError(context.Canceled) {
		t.Fatal("expected IsTimeoutError to be false for other errors")
	}
}

func TestJoinConnHandlersKeepsTimeouts(t *testing.T) {
	noop := func(*NSConn, Message) error { return nil }

	h := JoinConnHandlers(
		WithTimeout{ReadTimeout: time.Second, WriteTimeout: 2 * time.Second, PingInterval: 3 * time.Second, MaxMessageSize: 4, Events: Events{"a": noop}},
		Namespaces{"ns": Events{"b": noop}},
	)
	want := connSettings{readTimeout: time.Second, writeTimeout: 2 * time.Second, pingInterval: 3 * time.Second, maxMessageSize: 4}
	if got := getSettings(h); got != want {
		t.Fatalf("WithTimeout: expected %+v, got %+v", want, got)
	}
	nss := h.GetNamespaces()
	if nss[""]["a"] == nil || nss["ns"]["b"] == nil {
		t.Fatalf("expected both handlers' events, got %v", nss)
	}

	st := NewStruct(new(testStructStatic)).SetTimeouts(5*time.Second, 6*time.Second).SetPingInterval(7 * time.Second).SetMaxMessageSize(8)
	h2 := JoinConnHandlers(Events{"c": noop}, st)
	want2 := connSettings{readTimeout: 5 * time.Second, writeTimeout: 6 * time.Second, pingInterval: 7 * time.Second, maxMessageSize: 8}
	if got := getSettings(h2); got != want2 {
		t.Fatalf("Struct: expected %+v, got %+v", want2, got)
	}

	// a later non-zero setting wins; a zero one keeps the earlier value.
	h3 := JoinConnHandlers(WithTimeout{ReadTimeout: time.Second, WriteTimeout: time.Second}, WithTimeout{ReadTimeout: 2 * time.Second})
	if got := getSettings(h3); got.readTimeout != 2*time.Second || got.writeTimeout != time.Second {
		t.Fatalf("merge: expected read 2s and write 1s, got %+v", got)
	}

	// without settings the result stays a plain Namespaces.
	if _, ok := JoinConnHandlers(Events{"a": noop}, Namespaces{"ns": Events{"b": noop}}).(Namespaces); !ok {
		t.Fatal("expected Namespaces when no handler carries settings")
	}
}

// slowCloseSocket is a fakeSocket whose Close takes 200ms, like a close frame
// write to a peer that does not read.
type slowCloseSocket struct{ *fakeSocket }

func (s slowCloseSocket) Close(code int, reason string, timeout time.Duration) error {
	time.Sleep(200 * time.Millisecond)
	return s.fakeSocket.Close(code, reason, timeout)
}

// TestServerCloseTerminatesConcurrently closes a server with 20 connections
// whose close takes 200ms each. Close and Shutdown terminate them in
// parallel, so they return well before the 4s a sequential close would take.
func TestServerCloseTerminatesConcurrently(t *testing.T) {
	const conns = 20

	for _, name := range []string{"Close", "Shutdown"} {
		t.Run(name, func(t *testing.T) {
			srv := New(func(http.ResponseWriter, *http.Request) (Socket, error) {
				return nil, errors.New("not used")
			}, Namespaces{fakeNamespace: Events{}})
			srv.FireDisconnectAlways = true

			var disconnects atomic.Int32
			srv.OnDisconnect = func(*Conn) { disconnects.Add(1) }

			socks := make([]*fakeSocket, 0, conns)
			for range conns {
				s := newFakeSocket()
				socks = append(socks, s)

				c := newConn(slowCloseSocket{s}, srv.namespaces)
				c.server = srv
				if !srv.addConn(c, 0) {
					t.Fatal("addConn refused the connection")
				}
			}

			start := time.Now()
			if name == "Close" {
				srv.Close()
			} else if err := srv.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
			elapsed := time.Since(start)

			if elapsed > time.Second {
				t.Fatalf("expected %s to close %d connections concurrently, took %s", name, conns, elapsed)
			}
			for i, s := range socks {
				if got := s.closerCalls.Load(); got != 1 {
					t.Fatalf("connection %d: expected one close frame, got %d", i, got)
				}
			}
			if got := disconnects.Load(); got != conns {
				t.Fatalf("expected OnDisconnect for %d connections, got %d", conns, got)
			}
			if n := srv.GetTotalConnections(); n != 0 {
				t.Fatalf("expected 0 connections after %s, got %d", name, n)
			}
		})
	}
}

// TestRejectionError pins the close code a refused handshake gets. The wire
// tests in gorilla/ and gobwas/ observe 1001 for a closing server either way,
// because terminateAll's own Terminate reaches the socket first, so the
// ErrServerClosed branch is pinned here instead.
func TestRejectionError(t *testing.T) {
	plain := errors.New("not allowed")
	wrappedServerClosed := fmt.Errorf("upgrade: %w", ErrServerClosed)

	tests := []struct {
		name   string
		err    error
		code   int
		reason string
	}{
		{"plain error", plain, ClosePolicyViolation, "not allowed"},
		{"server closed", ErrServerClosed, CloseGoingAway, "server closed"},
		{"wrapped server closed", wrappedServerClosed, CloseGoingAway, "upgrade: server closed"},
		{"close error", CloseError{Code: 4003, Reason: "custom"}, 4003, "custom"},
		{"close error over a cause", CloseError{error: plain, Code: 4004}, 4004, "not allowed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rejectionError(tt.err)

			if code, reason := closeFrame(got); code != tt.code || reason != tt.reason {
				t.Fatalf("expected the close frame [%d] %q, got [%d] %q", tt.code, tt.reason, code, reason)
			}
			if !errors.Is(got, tt.err) {
				t.Fatalf("expected errors.Is to reach the original %v, got %v", tt.err, got)
			}
			if !IsCloseError(got) {
				t.Fatal("expected IsCloseError to be true")
			}
		})
	}

	// the identity of the original error survives, so a caller can still test
	// for its own sentinel.
	if !errors.Is(rejectionError(wrappedServerClosed), ErrServerClosed) {
		t.Fatal("expected errors.Is(ErrServerClosed) to hold through rejectionError")
	}
}
