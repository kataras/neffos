package ws

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

type broadcaster struct {
	message Message
	mu      *sync.Mutex
	awaiter unsafe.Pointer
}

func newBroadcaster() *broadcaster {
	ch := make(chan struct{})
	awaiter := unsafe.Pointer(&ch)
	return &broadcaster{
		mu:      new(sync.Mutex),
		awaiter: awaiter,
	}
}

func (b *broadcaster) getAwaiter() <-chan struct{} {
	ptr := atomic.LoadPointer(&b.awaiter)
	return *((*chan struct{})(ptr))
}

func (b *broadcaster) broadcast(msg Message) {
	b.mu.Lock()
	b.message = msg
	b.mu.Unlock()

	ch := make(chan struct{})
	old := atomic.SwapPointer(&b.awaiter, unsafe.Pointer(&ch))
	close(*(*chan struct{})(old))
}

// lock required.
func (b *broadcaster) wait() (msg Message) {
	ch := b.getAwaiter()
	b.mu.Unlock()
	<-ch
	msg = b.message
	b.mu.Lock()

	return
}

func (b *broadcaster) subscribe(fn func(<-chan struct{})) {
	ch := b.getAwaiter()
	b.mu.Unlock()
	fn(ch)
	b.mu.Lock()
}

func (b *broadcaster) waitUntilClosed(closeCh <-chan struct{}) (msg Message, ok bool) {
	b.subscribe(func(ch <-chan struct{}) {
		select {
		case <-ch:
			msg = b.message
			ok = true
		case <-closeCh:
		}
	})

	return
}

func (b *broadcaster) waitUntil(timeout time.Duration) (msg Message, ok bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	b.subscribe(func(ch <-chan struct{}) {
		select {
		case <-ch:
			msg = b.message
			ok = true
		case <-timer.C:
		}
	})

	return
}

func (b *broadcaster) waitWithContext(ctx context.Context) (msg Message, err error) {
	b.subscribe(func(ch <-chan struct{}) {
		select {
		case <-ch:
			msg = b.message
		case <-ctx.Done():
			err = ctx.Err()
		}
	})

	return
}
