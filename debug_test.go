package neffos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableDebugUnsupportedPrinter(t *testing.T) {
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

	EnableDebug(42) // an int has none of Debugf, Logf, Printf.

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
