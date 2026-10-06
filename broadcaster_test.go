package neffos

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBroadcasterDeliversMessages(t *testing.T) {
	b := newBroadcaster()
	closeCh := make(chan struct{})

	type result struct {
		msgs []Message
		ok   bool
	}
	resCh := make(chan result, 1)
	// take the head before the goroutine starts, as Upgrade does, so the
	// broadcast below is seen however late the goroutine runs.
	entry := b.head()
	go func() {
		msgs, _, ok := b.waitUntilClosed(entry, closeCh)
		resCh <- result{msgs, ok}
	}()

	want := []Message{{Namespace: "ns", Event: "e", Body: []byte("hi")}}
	b.broadcast(want)

	select {
	case got := <-resCh:
		if !got.ok {
			t.Fatalf("expected ok=true, got false")
		}
		if len(got.msgs) != len(want) || got.msgs[0].Event != want[0].Event {
			t.Fatalf("expected %v, got %v", want, got.msgs)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for broadcast delivery")
	}
}

func TestBroadcasterCloseChAborts(t *testing.T) {
	b := newBroadcaster()
	closeCh := make(chan struct{})

	type result struct {
		msgs []Message
		ok   bool
	}
	resCh := make(chan result, 1)
	go func() {
		msgs, _, ok := b.waitUntilClosed(b.head(), closeCh)
		resCh <- result{msgs, ok}
	}()

	close(closeCh)

	select {
	case got := <-resCh:
		if got.ok {
			t.Fatalf("expected ok=false on closeCh, got true")
		}
		if got.msgs != nil {
			t.Fatalf("expected nil msgs, got %v", got.msgs)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for close path")
	}
}

func TestBroadcasterConcurrentReceiversAndBroadcasts(t *testing.T) {
	b := newBroadcaster()

	const receivers = 32
	const broadcasts = 64

	var received atomic.Int64
	var wg sync.WaitGroup

	closeChs := make([]chan struct{}, receivers)

	// take the head before any receiver starts, as Upgrade does, so every
	// receiver sees every broadcast below however late it runs.
	head := b.head()

	// Each receiver consumes broadcasts in a loop. When closeCh fires they
	// exit. The test verifies that no receiver ever sees a nil messages slice
	// with ok=true (which would mean a broadcast was delivered without payload).
	for i := range receivers {
		closeChs[i] = make(chan struct{})
		wg.Add(1)
		go func(closeCh chan struct{}) {
			defer wg.Done()
			entry := head
			for {
				msgs, next, ok := b.waitUntilClosed(entry, closeCh)
				if !ok {
					return
				}
				if msgs == nil {
					t.Errorf("ok=true but msgs is nil: receiver woke without payload")
					return
				}
				received.Add(1)
				entry = next
			}
		}(closeChs[i])
	}

	var bwg sync.WaitGroup
	for i := range broadcasts {
		bwg.Add(1)
		go func(i int) {
			defer bwg.Done()
			b.broadcast([]Message{{Event: "evt", Body: []byte{byte(i)}}})
		}(i)
	}
	bwg.Wait()

	// every receiver follows the chain from the same head, so each one
	// delivers every broadcast; wait for the total rather than sleeping.
	const want = receivers * broadcasts
	deadline := time.Now().Add(5 * time.Second)
	for received.Load() < want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := received.Load(); got != want {
		t.Fatalf("expected %d deliveries, got %d", want, got)
	}

	// Tear down receivers and wait for them to exit.
	for _, ch := range closeChs {
		close(ch)
	}
	wg.Wait()
}

// TestBroadcastChainAdvances publishes two broadcasts before the consumer
// wakes. Following the chain, the consumer must see both, in order, and then
// wait on the entry after the second one.
func TestBroadcastChainAdvances(t *testing.T) {
	b := newBroadcaster()
	closeCh := make(chan struct{})

	entry := b.head()

	b.broadcast([]Message{{Event: "first"}})
	b.broadcast([]Message{{Event: "second"}})

	msgs, next, ok := b.waitUntilClosed(entry, closeCh)
	if !ok || len(msgs) != 1 || msgs[0].Event != "first" {
		t.Fatalf("expected the first broadcast, got ok=%v msgs=%v", ok, msgs)
	}

	msgs, next, ok = b.waitUntilClosed(next, closeCh)
	if !ok || len(msgs) != 1 || msgs[0].Event != "second" {
		t.Fatalf("expected the second broadcast, got ok=%v msgs=%v", ok, msgs)
	}

	if next != b.head() {
		t.Fatal("expected the consumer to wait on the current head after the last broadcast")
	}

	close(closeCh)
	if msgs, _, ok := b.waitUntilClosed(next, closeCh); ok || msgs != nil {
		t.Fatalf("expected ok=false and nil msgs after closeCh, got ok=%v msgs=%v", ok, msgs)
	}
}
