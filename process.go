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
type process struct {
	done uint32

	finished chan struct{}
	waiting  sync.WaitGroup
}

// Signal closes the channel.
func (p *process) Signal() {
	// if !atomic.CompareAndSwapUint32(&p.running, 1, 0) {
	// 	return // already finished.
	// }

	close(p.finished)
}

// Finished returns the read-only channel of `finished`.
// It gets fired when `Signal` is called.
func (p *process) Finished() <-chan struct{} {
	return p.finished
}

// Done calls the internal WaitGroup's `Done` method.
func (p *process) Done() {
	if !atomic.CompareAndSwapUint32(&p.done, 0, 1) {
		return
	}

	p.waiting.Done()
}

// Wait waits on the internal `WaitGroup`. See `Done` too.
func (p *process) Wait() {
	if atomic.LoadUint32(&p.done) == 1 {
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
	return atomic.LoadUint32(&p.done) == 1
}
