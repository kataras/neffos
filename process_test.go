package neffos

import (
	// "sync"
	"testing"
	"time"
)

func TestProcess(t *testing.T) {
	var testProcessName = "default"
	procs := newProcesses()

	worker := func() {
		defer procs.get(testProcessName).stop()
		if !procs.get(testProcessName).isRunning() {
			t.Fatalf("%s process should be running", testProcessName)
		}
		time.Sleep(1 * time.Second)
	}

	procs.get(testProcessName).start()
	go worker()

	procs.get(testProcessName).wait()
	if procs.get(testProcessName).isRunning() {
		t.Fatalf("%s process should be stopped", testProcessName)
	}
}
