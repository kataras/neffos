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
	go func() {
		msgs, ok := b.waitUntilClosed(closeCh)
		resCh <- result{msgs, ok}
	}()

	// give the receiver a moment to subscribe to the current entry.
	time.Sleep(10 * time.Millisecond)

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
		msgs, ok := b.waitUntilClosed(closeCh)
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

	// Each receiver consumes broadcasts in a loop. When closeCh fires they
	// exit. The test verifies that no receiver ever sees a nil messages slice
	// with ok=true (which would mean a broadcast was delivered without payload).
	for i := range receivers {
		closeChs[i] = make(chan struct{})
		wg.Add(1)
		go func(closeCh chan struct{}) {
			defer wg.Done()
			for {
				msgs, ok := b.waitUntilClosed(closeCh)
				if !ok {
					return
				}
				if msgs == nil {
					t.Errorf("ok=true but msgs is nil — receiver woke without payload")
					return
				}
				received.Add(1)
			}
		}(closeChs[i])
	}

	// give receivers time to subscribe.
	time.Sleep(20 * time.Millisecond)

	var bwg sync.WaitGroup
	for i := range broadcasts {
		bwg.Add(1)
		go func(i int) {
			defer bwg.Done()
			b.broadcast([]Message{{Event: "evt", Body: []byte{byte(i)}}})
		}(i)
	}
	bwg.Wait()

	// give receivers time to wake on the last broadcast.
	time.Sleep(50 * time.Millisecond)

	if got := received.Load(); got == 0 {
		t.Fatal("expected at least some deliveries, got zero")
	}

	// Tear down receivers and wait for them to exit.
	for _, ch := range closeChs {
		close(ch)
	}
	wg.Wait()
}
