package neffos

import (
	"fmt"
	"sync/atomic"
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
		time.Sleep(1 * time.Second)
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

func TestProcessSingalFinished(t *testing.T) {
	var testProcessName = "default"
	procs := newProcesses()
	p := procs.get(testProcessName)

	var count uint32

	worker := func() error {
		p.Start()
		defer p.Done()

		tc := time.NewTicker(time.Second)
		defer tc.Stop()

		for {
			select {
			case <-tc.C:
				atomic.AddUint32(&count, 1)
			case <-p.Finished():
				return nil
			}
		}
	}

	g := new(errgroup.Group)
	g.Go(worker)

	var sleepSecs uint32 = 2
	time.Sleep(time.Duration(sleepSecs) * time.Second)
	p.Signal()
	p.Wait()
	if !procs.get(testProcessName).isDone() {
		t.Fatalf("%s process should be stopped", testProcessName)
	}

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}

	if counts := atomic.LoadUint32(&count); counts != sleepSecs {
		t.Fatalf("%s process should tik-tok for %d seconds but: %d", testProcessName, sleepSecs, counts)
	}
}

func TestProcessDoneIdempotent(t *testing.T) {
	procs := newProcesses()
	p := procs.get("done-twice")
	p.Start()

	p.Done()
	// Second Done must be a no-op rather than panicking with "negative WaitGroup
	// counter".
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
