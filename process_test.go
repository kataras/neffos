package neffos

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

func TestProcessWaitDone(t *testing.T) {
	var testProcessName = "default"
	procs := newProcesses()
	p := procs.get(testProcessName)
	p.Start()

	worker := func() error {
		defer p.Done()
		if p.isDone() {
			return fmt.Errorf("%s process should be running", testProcessName)
		}
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	g := new(errgroup.Group)
	g.Go(worker)

	p.Wait()
	if !p.isDone() {
		t.Fatalf("%s process should be finished", testProcessName)
	}

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessDoneIdempotent(t *testing.T) {
	procs := newProcesses()
	p := procs.get("done-twice")
	p.Start()

	p.Done()
	// Second Done must be a no-op rather than panicking with "close of closed channel".
	p.Done()

	if !p.isDone() {
		t.Fatal("expected process to be done after Done()")
	}

	// Wait must return immediately since we're already done.
	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return immediately after Done")
	}
}

func TestProcessRestart(t *testing.T) {
	procs := newProcesses()
	p := procs.get("restart")

	// First run.
	p.Start()
	if p.isDone() {
		t.Fatal("expected process to be running right after Start")
	}
	p.Done()
	if !p.isDone() {
		t.Fatal("expected process to be done after Done")
	}
	p.Wait() // not running anymore, must return immediately.

	// Second run: Start again must make Wait block until the new Done.
	p.Start()
	if p.isDone() {
		t.Fatal("expected process to be running after the second Start")
	}

	waitReturned := make(chan struct{})
	go func() {
		p.Wait()
		close(waitReturned)
	}()

	select {
	case <-waitReturned:
		t.Fatal("Wait returned before the second Done was called")
	case <-time.After(50 * time.Millisecond):
	}

	p.Done()

	select {
	case <-waitReturned:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the second Done")
	}

	if !p.isDone() {
		t.Fatal("expected process to be done after the second Done")
	}
}

func TestProcessWaitWithoutStartReturns(t *testing.T) {
	procs := newProcesses()
	p := procs.get("never-started")

	if !p.isDone() {
		t.Fatal("expected a never-started process to report isDone")
	}

	done := make(chan struct{})
	go func() {
		p.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait blocked on a process that was never started")
	}
}

func TestProcessesGetSamePointerConcurrently(t *testing.T) {
	procs := newProcesses()
	const name = "shared"
	const goroutines = 50

	var (
		wg      sync.WaitGroup
		results = make([]*process, goroutines)
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = procs.get(name)
		}(i)
	}
	wg.Wait()

	first := results[0]
	if first == nil {
		t.Fatal("expected a non-nil process")
	}
	for i, p := range results {
		if p != first {
			t.Fatalf("goroutine %d got a different *process pointer than goroutine 0", i)
		}
	}
}
