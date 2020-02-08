package neffos

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

func TestProcess(t *testing.T) {
	var testProcessName = "default"
	procs := newProcesses()

	worker := func() error {
		defer procs.get(testProcessName).stop()
		if !procs.get(testProcessName).isRunning() {
			return fmt.Errorf("%s process should be running", testProcessName)
		}
		time.Sleep(1 * time.Second)
		return nil
	}

	procs.get(testProcessName).start()
	g := new(errgroup.Group)
	g.Go(worker)

	procs.get(testProcessName).wait()
	if procs.get(testProcessName).isRunning() {
		t.Fatalf("%s process should be stopped", testProcessName)
	}

	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
