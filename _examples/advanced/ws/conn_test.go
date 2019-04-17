package ws

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestEmitWithCallback(t *testing.T) {
	var (
		namespace   = "default"
		pingEvent   = "ping"
		pongMessage = []byte("PONG MESSAGE")
	)

	server := New(Namespaces{namespace: Events{
		pingEvent: func(c NSConn, msg Message) error {
			// c.Emit("event", pongMessage)
			return Reply(pongMessage) // changes only body; ns,event remains.
		}}})
	defer server.Close()

	httpServer := http.Server{
		Addr:    "localhost:8080",
		Handler: server,
	}
	defer httpServer.Close()
	go httpServer.ListenAndServe()
	time.Sleep(200 * time.Millisecond)

	client, err := Dial(nil, "ws://localhost:8080", Namespaces{namespace: Events{}})
	if err != nil {
		t.Fatal(err)
	}

	c, err := client.Connect(namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for i := 1; i <= 5; i++ {
		msg := c.Ask(pingEvent, nil)
		if msg.wait != uint64(i) {
			t.Fatalf("[%d] msg.wait should be %d but got %d", i, i, msg.wait)
		}

		if msg.Namespace != namespace {
			t.Fatalf("[%d] expected namespace to be %s but got %s instead", i, namespace, msg.Namespace)
		}

		if msg.Event != pingEvent {
			t.Fatalf("[%d] expected event to be %s but got %s instead", i, pingEvent, msg.Event)
		}

		if !bytes.Equal(msg.Body, pongMessage) {
			t.Fatalf("[%d] from callback: expected %s but got %s", i, string(pongMessage), string(msg.Body))
		}
	}

	msg := c.Ask(pingEvent, nil)
	if msg.wait != 1 {
		t.Fatalf("msg.wait should is always be one after all received but got %d", msg.wait)
	}

	if msg.Namespace != namespace {
		t.Fatalf("expected namespace to be %s but got %s instead", namespace, msg.Namespace)
	}

	if msg.Event != pingEvent {
		t.Fatalf("expected event to be %s but got %s instead", pingEvent, msg.Event)
	}

	if !bytes.Equal(msg.Body, pongMessage) {
		t.Fatalf("from callback: expected %s but got %s", string(pongMessage), string(msg.Body))
	}
}
