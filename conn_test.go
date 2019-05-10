package ws_test

import (
	"bytes"
	"reflect"
	"sync"
	"testing"

	"github.com/kataras/ws"
)

func TestConnect(t *testing.T) {
	// test valid and not valid namespace connection.

	var (
		namespace    = "default"
		onlyOnServer = "only_on_server"
		onlyOnClient = "only_on_client"
		emptyEvents  = ws.Events{}
	)

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{"": emptyEvents, namespace: emptyEvents, onlyOnServer: emptyEvents})
	defer teardownServer()

	err := runTestClient("localhost:8080", ws.Namespaces{"": emptyEvents, namespace: emptyEvents, onlyOnClient: emptyEvents},
		func(dialer string, client *ws.Client) {
			defer client.Close()

			// should success, empty namespace naming is allowed and it's defined on both server and client-side.
			c, err := client.Connect(nil, "")
			if err != nil {
				t.Fatal(err)
			}

			// should success, namespace exists in server-side and it's defined on client-side.
			c, err = client.Connect(nil, namespace)
			if err != nil {
				t.Fatal(err)
			}

			c, err = client.Connect(nil, onlyOnServer)
			if err == nil || c != nil {
				t.Fatalf("%s namespace connect should fail, namespace exists on server but not defined at client-side", onlyOnServer)
			}

			c, err = client.Connect(nil, onlyOnClient)
			if err == nil || c != nil {
				t.Fatalf("%s namespace connect should fail, namespace defined on client but not exists at server-side.", onlyOnClient)
			}

		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAsk(t *testing.T) {
	var (
		namespace   = "default"
		pingEvent   = "ping"
		pongMessage = []byte("PONG MESSAGE")
	)

	testMessage := func(dialer string, i int, msg ws.Message) {
		if msg.Namespace != namespace {
			t.Fatalf("[%s] [%d] expected namespace to be %s but got %s instead", dialer, i, namespace, msg.Namespace)
		}

		if msg.Event != pingEvent {
			t.Fatalf("[%s] [%d] expected event to be %s but got %s instead", dialer, i, pingEvent, msg.Event)
		}

		if !bytes.Equal(msg.Body, pongMessage) {
			t.Fatalf("[%s] [%d] from callback: expected %s but got %s", dialer, i, string(pongMessage), string(msg.Body))
		}
	}

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{namespace: ws.Events{
		pingEvent: func(c *ws.NSConn, msg ws.Message) error {
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

		for i := 1; i <= 5; i++ {
			msg, err := c.Ask(nil, pingEvent, nil)
			if err != nil {
				t.Fatal(err)
			}
			testMessage(dialer, i, msg)
		}

		msg, err := c.Ask(nil, pingEvent, nil)
		if err != nil {
			t.Fatal(err)
		}
		testMessage(dialer, -1, msg)
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestOnAnyEvent(t *testing.T) {
	var (
		namespace       = "default"
		expectedMessage = ws.Message{
			Namespace: namespace,
			Event:     "an_event",
			Body:      []byte("a_body"),
		}
		wg          sync.WaitGroup // a pure check for client's `Emit` to fire (`Ask` don't need this).
		testMessage = func(msg ws.Message) {
			// if !reflect.DeepEqual(msg, expectedMessage) { no becasue of Ask.wait.
			if msg.Namespace != expectedMessage.Namespace ||
				msg.Event != expectedMessage.Event ||
				!bytes.Equal(msg.Body, expectedMessage.Body) {

				t.Fatalf("expected message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
			}
		}
	)

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{namespace: ws.Events{
		ws.OnAnyEvent: func(c *ws.NSConn, msg ws.Message) error {
			if ws.IsSystemEvent(msg.Event) { // skip connect/disconnect messages.
				return nil
			}

			return ws.Reply(msg.Body)
		}}})
	defer teardownServer()

	err := runTestClient("localhost:8080", ws.Namespaces{namespace: ws.Events{
		expectedMessage.Event: func(c *ws.NSConn, msg ws.Message) error {
			defer wg.Done()
			testMessage(msg)

			return nil
		},
	}}, func(dialer string, client *ws.Client) {
		defer client.Close()

		c, err := client.Connect(nil, namespace)
		if err != nil {
			t.Fatal(err)
		}

		wg.Add(1)
		c.Emit(expectedMessage.Event, expectedMessage.Body)
		wg.Wait()

		msg, err := c.Ask(nil, expectedMessage.Event, expectedMessage.Body)
		if err != nil {
			t.Fatal(err)
		}
		testMessage(msg)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnNativeMessage(t *testing.T) {
	var (
		wg            sync.WaitGroup
		namespace     = "" // empty namespace and OnNativeMessage event defined to allow native websocket messages to come through.
		nativeMessage = []byte("this is a native/raw websocket message")
		events        = ws.Events{
			ws.OnNativeMessage: func(c *ws.NSConn, msg ws.Message) error {
				defer wg.Done()
				expectedMessage := ws.Message{
					Event:    ws.OnNativeMessage,
					Body:     nativeMessage,
					IsNative: true,
				}

				if !reflect.DeepEqual(expectedMessage, msg) {
					t.Fatalf("expected a native message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
				}

				return nil
			},
		}
	)

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{namespace: events})
	defer teardownServer()

	err := runTestClient("localhost:8080", ws.Namespaces{namespace: events},
		func(dialer string, client *ws.Client) {
			defer client.Close()

			c, err := client.Connect(nil, namespace)
			if err != nil {
				t.Fatal(err)
			}

			// Ask is not available on native websocket messages of course.
			wg.Add(1)
			c.Conn.Write(ws.Message{
				Body:     nativeMessage,
				IsNative: true,
			})
		})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSimultaneouslyEventsRoutines(t *testing.T) {
	// test multiple goroutines sending events, it should work because the lib it is designed to take care of these things.
	var (
		wg               sync.WaitGroup
		namespace        = "namespace1"
		event1           = "event1"
		event2           = "event2"
		event3           = "event3"
		expectedMessages = map[string]ws.Message{
			event1: ws.Message{Namespace: namespace,
				Event: event1,
				Body:  []byte("body1"),
			},
			event2: ws.Message{
				Namespace: namespace,
				Event:     event2,
				Body:      []byte("body2"),
			},
			event3: ws.Message{
				Namespace: namespace,
				Event:     event3,
				Body:      []byte("body2"),
			},
		}
		events = ws.Events{
			ws.OnAnyEvent: func(c *ws.NSConn, msg ws.Message) error {
				if ws.IsSystemEvent(msg.Event) {
					return nil
				}

				expectedMessage := expectedMessages[msg.Event]

				if !reflect.DeepEqual(msg, expectedMessage) {
					t.Fatalf("expected message:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
				}

				if c.Conn.IsClient() {
					// wait for the reply for server before done with this event test.
					defer wg.Done()
				} else {
					return ws.Reply(msg.Body)
				}

				return nil
			},
		}
	)

	teardownServer := runTestServer("localhost:8080", ws.Namespaces{namespace: events})
	defer teardownServer()

	err := runTestClient("localhost:8080", ws.Namespaces{namespace: events},
		func(dialer string, client *ws.Client) {
			defer client.Close()

			c, err := client.Connect(nil, namespace)
			if err != nil {
				t.Fatal(err)
			}

			send := func(event string) {
				wg.Add(1)
				msg := expectedMessages[event]
				c.Emit(msg.Event, msg.Body)
			}

			for i := 0; i < 5; i++ {
				go send(event1)
				go send(event2)
				go send(event3)
			}

			for i := 0; i < 5; i++ {
				go send(event3)
				go send(event2)
				go send(event1)
			}

			wg.Wait()
		})
	if err != nil {
		t.Fatal(err)
	}
}
