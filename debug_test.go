package neffos

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableDebugNilPrinterFallsBackToStderr(t *testing.T) {
	if debugEnabled() {
		t.Skip("debug mode already enabled by another test")
	}

	// the fallback logger writes to os.Stderr; capture it to keep output clean.
	stderrPath := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = oldStderr
		debugPrinter = nil
	})

	EnableDebug(nil) // nil falls back to a stderr logger.

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Debugf panicked with an unsupported printer: %v", r)
			}
		}()
		Debugf("value %d", 7)
	}()

	os.Stderr = oldStderr
	f.Close()

	out, err := os.ReadFile(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "value 7") {
		t.Fatalf("expected the default logger to print the debug message, got %q", out)
	}
}

func TestEnableDebugPrinterFunc(t *testing.T) {
	if debugEnabled() {
		t.Skip("debug mode already enabled by another test")
	}
	t.Cleanup(func() { debugPrinter = nil })

	var got string
	EnableDebug(PrinterFunc(func(format string, args ...any) {
		got = fmt.Sprintf(format, args...)
	}))

	Debugf("value %d", 7)
	if got != "value 7" {
		t.Fatalf("expected the PrinterFunc to receive the message, got %q", got)
	}

	// lazy arguments are evaluated only in debug mode.
	Debugf("lazy %s", func() dargs { return dargs{"ok"} })
	if got != "lazy ok" {
		t.Fatalf("expected lazy args to be expanded, got %q", got)
	}

	// a *log.Logger is a Printer.
	var _ Printer = log.New(io.Discard, "", 0)

	// debugEach visits every entry of a map.
	seen := map[string]int{}
	debugEach(map[string]int{"a": 1, "b": 2}, func(k string, v int) { seen[k] = v })
	if len(seen) != 2 || seen["a"] != 1 || seen["b"] != 2 {
		t.Fatalf("unexpected visits %v", seen)
	}
}
