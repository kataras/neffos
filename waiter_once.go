package neffos

import (
	"sync"
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
// One-shot semantics: the first unwait stores its error and releases every
// current and future wait; later unwait calls are no-ops. Any number of
// goroutines may wait.
//
// See `Server#ServeHTTP`, `Conn#Connect`, `Conn#Write`, `Conn#sendClientACK` and `Conn#handleACK`.
type waiterOnce struct {
	once  sync.Once
	ready atomic.Uint32
	// err is written once, before ready is set and ch is closed, so it may be
	// read after isReady reports true or after ch is closed.
	err error
	ch  chan struct{}
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

// wait blocks until the first unwait and returns its error.
// If unwait was already called it returns immediately.
func (w *waiterOnce) wait() error {
	if w == nil {
		return nil
	}

	<-w.ch
	return w.err
}

// unwait stores err and releases every wait. Only the first call has an effect.
func (w *waiterOnce) unwait(err error) {
	if w == nil {
		return
	}

	w.once.Do(func() {
		w.err = err
		w.ready.Store(1)
		close(w.ch)
	})
}
