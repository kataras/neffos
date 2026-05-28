package neffos

import (
	"sync"
	"sync/atomic"
)

// processes is a collection of `process`.
type processes struct {
	entries map[string]*process
	locker  *sync.RWMutex
}

func newProcesses() *processes {
	return &processes{
		entries: make(map[string]*process),
		locker:  new(sync.RWMutex),
	}
}

func (p *processes) get(name string) *process {
	p.locker.RLock()
	entry := p.entries[name]
	p.locker.RUnlock()

	if entry == nil {
		entry = &process{
			finished: make(chan struct{}),
		}

		p.locker.Lock()
		p.entries[name] = entry
		p.locker.Unlock()
	}

	return entry
}

// process is used on connections on specific actions that needs to wait for an answer from the other side.
// Take for example the `Conn#handleMessage.tryNamespace` which waits for `Conn#askConnect` to finish on the specific namespace.
//
// Lifecycle: Start -> Done -> [optional] Signal. Done is idempotent (subsequent
// calls are no-ops). Signal must be called at most once; double-call panics.
// Wait blocks until Done; after Done it returns immediately.
type process struct {
	done atomic.Uint32

	finished chan struct{}
	waiting  sync.WaitGroup
}

// Signal closes the finished channel. Must be called at most once per process —
// double-Signal panics because Go closes a closed channel with a runtime panic.
func (p *process) Signal() {
	close(p.finished)
}

// Finished returns the read-only channel of `finished`.
// It gets fired when `Signal` is called.
func (p *process) Finished() <-chan struct{} {
	return p.finished
}

// Done releases waiters on this process. Idempotent: subsequent calls are no-ops.
func (p *process) Done() {
	if !p.done.CompareAndSwap(0, 1) {
		return
	}

	p.waiting.Done()
}

// Wait blocks until Done has been called. If Done was already called, Wait
// returns immediately.
func (p *process) Wait() {
	if p.done.Load() == 1 {
		return
	}
	p.waiting.Wait()
}

// Start makes future `Wait` calls to hold until `Done`.
func (p *process) Start() {
	p.waiting.Add(1)
}

// isDone reports whether process is finished.
func (p *process) isDone() bool {
	return p.done.Load() == 1
}
