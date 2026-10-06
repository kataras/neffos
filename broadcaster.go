package neffos

import (
	"sync/atomic"
)

// broadcaster is the async fan-out point used by Server.Broadcast.
//
// Broadcasts form a chain of entries. current is the newest entry, the one
// no broadcast has filled yet. Each broadcast swaps in a fresh entry, attaches
// its messages and the fresh entry (as next) to the previous one, and then
// closes the previous entry's done channel. The caller never waits for a
// receiver.
//
// A receiver starts at head() and, after publishing an entry's messages, moves
// on to that entry's next. It therefore sees every broadcast made after it
// called head(), in order, even when several broadcasts land while it is still
// writing an earlier one.
//
// Backpressure: an entry stays reachable until every receiver has moved past
// it, and the garbage collector frees it after that, so no explicit cleanup is
// needed. A slow connection keeps the chain from its position to current
// alive, which is the queue of messages it still has to write. That memory is
// the cost of not dropping messages for a connection that is behind. Once the
// connection closes, its receiver stops and releases its place in the chain.
//
// The contract: messages and next are written before close(done), so any
// goroutine that wakes from <-done sees both (channel close establishes
// happens-before).
type broadcaster struct {
	current atomic.Pointer[broadcastEntry]
}

// broadcastEntry holds the payload for a single broadcast generation.
// done is closed exactly once when the broadcast that owns this entry completes.
type broadcastEntry struct {
	done     chan struct{}
	messages []Message
	next     *broadcastEntry // set before done is closed.
}

func newBroadcaster() *broadcaster {
	b := &broadcaster{}
	b.current.Store(&broadcastEntry{done: make(chan struct{})})
	return b
}

// broadcast publishes msgs to every receiver waiting on the current entry or
// on an earlier one. It never blocks on slow receivers.
func (b *broadcaster) broadcast(msgs []Message) {
	next := &broadcastEntry{done: make(chan struct{})}
	prev := b.current.Swap(next)
	prev.messages = msgs
	prev.next = next
	close(prev.done)
}

// head returns the entry a new receiver should start waiting on: the next
// broadcast fills it.
func (b *broadcaster) head() *broadcastEntry {
	return b.current.Load()
}

// waitUntilClosed waits for entry to be published or for closeCh to fire.
// On a broadcast it returns the entry's messages, the entry to wait on next,
// and true. On closeCh it returns (nil, nil, false).
func (b *broadcaster) waitUntilClosed(entry *broadcastEntry, closeCh <-chan struct{}) ([]Message, *broadcastEntry, bool) {
	select {
	case <-entry.done:
		return entry.messages, entry.next, true
	case <-closeCh:
		return nil, nil, false
	}
}
