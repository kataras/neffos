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
		pongEvent   = "pong"
		pongMessage = []byte("PONG MESSAGE")
	)

	server := New(Namespaces{namespace: Events{
		"ping": func(c NSConn, msg Message) error {
			c.Emit(pongEvent, pongMessage)
			return nil
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

	c.EmitWithCallback("ping", nil, func(msg Message) error {
		if msg.Namespace != namespace {
			t.Fatalf("expected namespace to be %s but got %s instead", namespace, msg.Namespace)
		}

		if msg.Event != pongEvent {
			t.Fatalf("expected event to be %s but got %s instead", pongEvent, msg.Event)
		}

		if !bytes.Equal(msg.Body, pongMessage) {
			t.Fatalf("from callback: expected %s but got %s", string(pongMessage), string(msg.Body))
		}
		return nil
	})
}
