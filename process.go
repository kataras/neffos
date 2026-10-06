package neffos

import (
	"sync"
)

// processes is a collection of `process`.
type processes struct {
	entries map[string]*process
	mu      sync.Mutex
}

func newProcesses() *processes {
	return &processes{
		entries: make(map[string]*process),
	}
}

func (p *processes) get(name string) *process {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.entries[name]
	if !ok {
		entry = new(process)
		p.entries[name] = entry
	}

	return entry
}

// process is used on connections on specific actions that needs to wait for an answer from the other side.
// Take for example the `Conn#handleMessage.tryNamespace` which waits for `Conn#askConnect` to finish on the specific namespace.
//
// A process is restartable: Start begins a run, Done ends it and releases any
// waiters, and Start may be called again afterwards to begin a new run. Wait
// blocks only while a run is in progress; it returns immediately otherwise,
// including for a process that was never started.
type process struct {
	mu      sync.Mutex
	running bool
	done    chan struct{}
}

// Start marks the process as running, so that `Wait` blocks until the matching `Done`.
// Safe to call again after a previous `Done` to begin a new run.
func (p *process) Start() {
	p.mu.Lock()
	p.running = true
	p.done = make(chan struct{})
	p.mu.Unlock()
}

// Done ends the current run and releases any goroutines blocked in `Wait`.
// It is a no-op if the process isn't currently running (double `Done`, or
// `Done` without a prior `Start`).
func (p *process) Done() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	p.running = false
	done := p.done
	p.mu.Unlock()

	close(done)
}

// Wait blocks until `Done` is called, but only while the process is running;
// otherwise it returns immediately.
func (p *process) Wait() {
	p.mu.Lock()
	running, done := p.running, p.done
	p.mu.Unlock()

	if running {
		<-done
	}
}

// isDone reports whether the process is not currently running.
func (p *process) isDone() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return !p.running
}
