package neffos

import (
	"sync/atomic"
)

// broadcaster is the async fan-out point used by Server.Broadcast.
//
// It does not block the caller while waiting for slow receivers: each broadcast
// atomically swaps in a fresh entry and signals the previous one. Receivers
// observe the messages tied to the entry they were waiting on, even when
// broadcasts overlap.
//
// The contract: broadcastEntry.messages is written before close(done), so any
// goroutine that wakes from <-done sees the messages associated with that exact
// broadcast call (channel close establishes happens-before).
type broadcaster struct {
	current atomic.Pointer[broadcastEntry]
}

// broadcastEntry holds the payload for a single broadcast generation.
// done is closed exactly once when the broadcast that owns this entry completes.
type broadcastEntry struct {
	done     chan struct{}
	messages []Message
}

func newBroadcaster() *broadcaster {
	b := &broadcaster{}
	b.current.Store(&broadcastEntry{done: make(chan struct{})})
	return b
}

// broadcast publishes msgs to every receiver currently blocked in waitUntilClosed.
// It never blocks on slow receivers.
func (b *broadcaster) broadcast(msgs []Message) {
	next := &broadcastEntry{done: make(chan struct{})}
	prev := b.current.Swap(next)
	prev.messages = msgs
	close(prev.done)
}

// waitUntilClosed blocks until the next broadcast completes or closeCh fires.
// Returns (messages, true) on a broadcast and (nil, false) on closeCh.
func (b *broadcaster) waitUntilClosed(closeCh <-chan struct{}) ([]Message, bool) {
	entry := b.current.Load()
	select {
	case <-entry.done:
		return entry.messages, true
	case <-closeCh:
		return nil, false
	}
}
