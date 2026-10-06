package neffos_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kataras/neffos"
)

// dial connects one *neffos.Client per backend in `dialers` to ts and calls
// fn with each. A dial failure fails the test immediately; dial is only ever
// called from a test body (never spawned on its own goroutine), so t.Fatal
// here is safe.
//
// dial owns closing every client it hands out: it registers t.Cleanup(client.Close)
// right after a successful Dial, before calling fn, so a client is always
// closed deterministically even if fn (or the test) never calls Close itself.
// Conn.Close is idempotent (guarded by a CompareAndSwap), so a test body that
// also does `defer client.Close()` is harmless.
func (ts *testServers) dial(t *testing.T, handler neffos.ConnHandler, fn func(backend string, c *neffos.Client)) {
	t.Helper()

	for backend, dialer := range dialers {
		client, err := neffos.Dial(context.TODO(), dialer, fmt.Sprintf("ws://%s/%s", ts.addr, backend), handler)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(client.Close)

		fn(backend, client)
	}
}

func TestClientConn(t *testing.T) {
	ts := newTestServers(t, neffos.Namespaces{"default": neffos.Events{}})

	ts.dial(t, neffos.Namespaces{"default": neffos.Events{}}, func(backend string, client *neffos.Client) {
		conn := client.Conn()
		if conn == nil {
			t.Fatalf("%s: expected a non-nil Conn", backend)
		}
		if !conn.IsClient() || conn.ID() != client.ID {
			t.Fatalf("%s: expected the client-side Conn with ID %q, got %q", backend, client.ID, conn.ID())
		}
		if err := conn.Err(); err != nil {
			t.Fatalf("%s: expected a nil Err() while open, got %v", backend, err)
		}

		client.Close()
		select {
		case <-client.NotifyClose:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: NotifyClose did not fire", backend)
		}

		if got := neffos.CloseStatus(client.Conn().Err()); got != neffos.CloseNormalClosure {
			t.Fatalf("%s: expected Err() code %d after Close, got %d", backend, neffos.CloseNormalClosure, got)
		}
	})
}
