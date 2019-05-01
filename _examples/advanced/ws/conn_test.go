package ws_test

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	gorillaWs "github.com/gorilla/websocket"
	"github.com/kataras/fastws/_examples/advanced/ws"
	gorilla "github.com/kataras/fastws/_examples/advanced/ws/gorilla"
)

func TestEmitWithCallback(t *testing.T) {
	var (
		namespace   = "default"
		pingEvent   = "ping"
		pongMessage = []byte("PONG MESSAGE")
	)

	// server := ws.New(gobwas.Upgrader(gobwasWs.HTTPUpgrader{}), ws.Namespaces{namespace: ws.Events{
	// 	pingEvent: func(c ws.NSConn, msg ws.Message) error {
	// 		// c.Emit("event", pongMessage)
	// 		return ws.Reply(pongMessage) // changes only body; ns,event remains.
	// 	}}})
	server := ws.New(gorilla.Upgrader(gorillaWs.Upgrader{}), ws.Namespaces{namespace: ws.Events{
		pingEvent: func(c ws.NSConn, msg ws.Message) error {
			// c.Emit("event", pongMessage)
			return ws.Reply(pongMessage) // changes only body; ns,event remains.
		}}})
	defer server.Close()

	httpServer := http.Server{
		Addr:    "localhost:8080",
		Handler: server,
	}
	defer httpServer.Close()
	go httpServer.ListenAndServe()
	time.Sleep(200 * time.Millisecond)

	client, err := ws.Dial(gorilla.Dialer(gorillaWs.DefaultDialer, make(http.Header)), nil, "ws://localhost:8080", ws.Namespaces{namespace: ws.Events{}})
	if err != nil {
		t.Fatal(err)
	}

	c, err := client.Connect(nil, namespace)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for i := 1; i <= 5; i++ {
		msg := c.Ask(nil, pingEvent, nil)
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

	msg := c.Ask(nil, pingEvent, nil)

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
