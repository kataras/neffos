package ws_test

import (
	"bytes"
	"testing"

	"github.com/kataras/fastws/_examples/advanced/ws"
)

func TestEmitWithCallback(t *testing.T) {
	var (
		namespace   = "default"
		pingEvent   = "ping"
		pongMessage = []byte("PONG MESSAGE")
	)

	testMessage := func(dialer string, i int, msg ws.Message) {
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

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{namespace: ws.Events{
		pingEvent: func(c ws.NSConn, msg ws.Message) error {
			// c.Emit("event", pongMessage)
			return ws.Reply(pongMessage) // changes only body; ns,event remains.
		}}})
	defer teardownServer()

	err := runTestClient("localhost:8080", ws.Namespaces{namespace: ws.Events{}}, func(dialer string, client *ws.Client) {
		defer client.Close()

		c, err := client.Connect(nil, namespace)
		if err != nil {
			t.Fatal(err)
		}

		// loops := 5
		// wg := new(sync.WaitGroup)
		// wg.Add(loops)
		// for i := 1; i <= loops; i++ {
		// 	go func(i int) {
		// 		defer wg.Done()
		// 		msg := c.Ask(nil, pingEvent, nil)
		// 		testMessage(dialer, i, msg)
		// 	}(i)
		// }

		// wg.Wait()

		for i := 1; i <= 5; i++ {
			msg := c.Ask(nil, pingEvent, nil)
			testMessage(dialer, i, msg)
		}

		msg := c.Ask(nil, pingEvent, nil)
		testMessage(dialer, -1, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
}
