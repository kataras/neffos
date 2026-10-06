package neffos

import (
	"errors"
	"testing"
	"time"
)

// TestWaiterOnceNoMissedSignal races wait against unwait many times. A wait
// that misses a concurrent unwait blocks forever, so each round is bounded by
// a timeout. Every round also checks that the error passed to unwait reaches
// wait.
func TestWaiterOnceNoMissedSignal(t *testing.T) {
	const rounds = 20000

	errWant := errors.New("handshake failed")

	for i := range rounds {
		w := newWaiterOnce()

		var want error
		if i%2 == 1 {
			want = errWant
		}

		start := make(chan struct{})
		got := make(chan error, 1)

		go func() {
			<-start
			got <- w.wait()
		}()
		go func() {
			<-start
			w.unwait(want)
		}()

		close(start)

		select {
		case err := <-got:
			if err != want {
				t.Fatalf("round %d: expected wait to return %v, got %v", i, want, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: wait missed a concurrent unwait", i)
		}
	}
}

// TestWaiterOnceSecondWaiterBlocks checks that every caller of wait blocks
// until unwait, not only the first one.
func TestWaiterOnceSecondWaiterBlocks(t *testing.T) {
	w := newWaiterOnce()

	const waiters = 4
	got := make(chan error, waiters)
	for range waiters {
		go func() { got <- w.wait() }()
	}

	select {
	case err := <-got:
		t.Fatalf("wait returned %v before unwait", err)
	case <-time.After(50 * time.Millisecond):
	}

	errWant := errors.New("closed")
	w.unwait(errWant)
	w.unwait(nil) // a second unwait must not change the stored error or panic.

	for range waiters {
		select {
		case err := <-got:
			if err != errWant {
				t.Fatalf("expected %v, got %v", errWant, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("wait did not return after unwait")
		}
	}

	if err := w.wait(); err != errWant {
		t.Fatalf("wait after unwait: expected %v, got %v", errWant, err)
	}
}
