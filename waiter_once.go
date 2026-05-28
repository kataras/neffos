package neffos

import (
	"sync/atomic"
)

// waiterOnce is used on the server and client-side connections to describe the readiness of handling messages.
// For both sides if Reading is errored it returns the error back to the `waiterOnce#wait()`.
// For server-side:
// It waits until error from `OnConnected` (if exists) or first Write action (i.e `Connect` on `OnConnected`).
//
// For client-side:
// It waits until ACK is done, if server sent an error then it returns the error to the `Client#Dial`.
//
// One-shot semantics: calling unwait twice is safe, calling wait after unwait
// returns immediately with the stored error (or nil).
//
// See `Server#ServeHTTP`, `Conn#Connect`, `Conn#Write`, `Conn#sendClientACK` and `Conn#handleACK`.
type waiterOnce struct {
	locked atomic.Uint32
	ready  atomic.Uint32
	err    error
	ch     chan struct{}
}

func newWaiterOnce() *waiterOnce {
	return &waiterOnce{
		ch: make(chan struct{}),
	}
}

func (w *waiterOnce) isReady() bool {
	if w == nil {
		return true
	}

	return w.ready.Load() > 0
}

// waits and returns any error from the `unwait`,
// but if `unwait` called before `wait` then it returns immediately.
func (w *waiterOnce) wait() error {
	if w == nil {
		return nil
	}

	if w.isReady() {
		return w.err // no need to wait.
	}

	if w.locked.CompareAndSwap(0, 1) {
		<-w.ch
	}

	return w.err
}

func (w *waiterOnce) unwait(err error) {
	if w == nil || w.isReady() {
		return
	}

	w.err = err
	// at any case mark it as ready for future `wait` call to exit immediately.
	w.ready.Store(1)
	if w.locked.CompareAndSwap(1, 0) { // unlock once.
		close(w.ch)
	}
}
