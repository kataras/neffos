package neffos_test

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/kataras/neffos"
)

func TestConnect(t *testing.T) {
	// test valid and not valid namespace connection.

	var (
		namespace                      = "default"
		onlyOnServer                   = "only_on_server"
		onlyOnClient                   = "only_on_client"
		namespaceThatShouldErrOnServer = "no_access_by_server"
		namespaceThatShouldErrOnClient = "no_access_by_client"
		emptyEvents                    = neffos.Events{}
	)

	ts := newTestServers(t, neffos.Namespaces{
		"":           emptyEvents,
		namespace:    emptyEvents,
		onlyOnServer: emptyEvents,
		namespaceThatShouldErrOnServer: neffos.Events{
			neffos.OnNamespaceConnect: func(c *neffos.NSConn, msg neffos.Message) error {
				return neffos.ErrBadNamespace
			},
		},
		namespaceThatShouldErrOnClient: emptyEvents,
	})

	ts.dial(t, neffos.Namespaces{
		"":           emptyEvents,
		namespace:    emptyEvents,
		onlyOnClient: emptyEvents,
		namespaceThatShouldErrOnServer: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				// Must never fire: the server rejects this namespace, so the
				// failure should surface at the `client.Connect` call below,
				// not here. This closure runs on the connection's own
				// goroutine, so t.Error (not t.Fatal) is required.
				t.Errorf("%s namespace shouldn't be accessible to the client to connect", namespaceThatShouldErrOnServer)
				return nil
			},
		},
		namespaceThatShouldErrOnClient: neffos.Events{
			neffos.OnNamespaceConnect: func(c *neffos.NSConn, msg neffos.Message) error {
				return neffos.ErrBadNamespace
			},
		},
	},
		func(backend string, client *neffos.Client) {
			defer client.Close()

			// should success, empty namespace naming is allowed and it's defined on both server and client-side.
			_, err := client.Connect(context.TODO(), "")
			if err != nil {
				t.Fatal(err)
			}

			// should success, namespace exists in server-side and it's defined on client-side.
			_, err = client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}

			c, err := client.Connect(context.TODO(), onlyOnServer)
			if err == nil || c != nil {
				t.Fatalf("%s namespace connect should fail, namespace exists on server but not defined at client-side", onlyOnServer)
			}

			c, err = client.Connect(context.TODO(), onlyOnClient)
			if err == nil || c != nil {
				t.Fatalf("%s namespace connect should fail, namespace defined on client but not exists at server-side.", onlyOnClient)
			}

			_, err = client.Connect(context.TODO(), namespaceThatShouldErrOnServer)
			if err != neffos.ErrBadNamespace {
				t.Fatalf("%s namespace connect should give a remote error by the server of the neffos.ErrBadNamespace exactly (it's a typed error which its text is converted to error when deserialized) but got: %v", namespaceThatShouldErrOnServer, err)
			}

			_, err = client.Connect(context.TODO(), namespaceThatShouldErrOnClient)
			if err != neffos.ErrBadNamespace {
				t.Fatalf("%s namespace connect should give a local event's error by the client of the neffos.ErrBadNamespace but got: %v", namespaceThatShouldErrOnServer, err)
			}

		})
}

func TestAsk(t *testing.T) {
	var (
		namespace   = "default"
		pingEvent   = "ping"
		pongMessage = []byte("PONG MESSAGE")
	)

	testMessage := func(dialer string, i int, msg neffos.Message) {
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

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{
		pingEvent: func(c *neffos.NSConn, msg neffos.Message) error {
			// c.Emit("event", pongMessage)
			return neffos.Reply(pongMessage) // changes only body; ns,event remains.
		}}})

	ts.dial(t, neffos.Namespaces{namespace: neffos.Events{}}, func(backend string, client *neffos.Client) {
		defer client.Close()

		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}

		for i := 1; i <= 5; i++ {
			msg, err := c.Ask(context.TODO(), pingEvent, nil)
			if err != nil {
				t.Fatal(err)
			}
			testMessage(backend, i, msg)
		}

		msg, err := c.Ask(context.TODO(), pingEvent, nil)
		if err != nil {
			t.Fatal(err)
		}
		testMessage(backend, -1, msg)
	})
}
func TestOnAnyEvent(t *testing.T) {
	var (
		namespace       = "default"
		expectedMessage = neffos.Message{
			Namespace: namespace,
			Event:     "an_event",
			Body:      []byte("a_body"),
		}
		wg          sync.WaitGroup // a pure check for client's `Emit` to fire (`Ask` don't need this).
		testMessage = func(msg neffos.Message) {
			// if !reflect.DeepEqual(msg, expectedMessage) { no because of Ask.wait.
			if msg.Namespace != expectedMessage.Namespace ||
				msg.Event != expectedMessage.Event ||
				!bytes.Equal(msg.Body, expectedMessage.Body) {

				// testMessage also runs on the client connection's own
				// goroutine (via the registered event handler below), so it
				// must use t.Error, not t.Fatal.
				t.Errorf("expected message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
			}
		}
	)

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{
		neffos.OnAnyEvent: func(c *neffos.NSConn, msg neffos.Message) error {
			if neffos.IsSystemEvent(msg.Event) { // skip connect/disconnect messages.
				return nil
			}

			return neffos.Reply(msg.Body)
		}}})

	ts.dial(t, neffos.Namespaces{namespace: neffos.Events{
		expectedMessage.Event: func(c *neffos.NSConn, msg neffos.Message) error {
			defer wg.Done()
			testMessage(msg)

			return nil
		},
	}}, func(backend string, client *neffos.Client) {
		defer client.Close()

		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}

		wg.Add(1)
		c.Emit(expectedMessage.Event, expectedMessage.Body)
		wg.Wait()

		msg, err := c.Ask(context.TODO(), expectedMessage.Event, expectedMessage.Body)
		if err != nil {
			t.Fatal(err)
		}
		testMessage(msg)
	})
}

func TestOnNativeMessageAndMessageError(t *testing.T) {
	var (
		wg                             sync.WaitGroup
		namespace                      = "" // empty namespace and OnNativeMessage event defined to allow native websocket messages to come through.
		eventThatWillGiveErrorByServer = "event_error_server"
		eventErrorText                 = "this event will give error by server"
		nativeMessage                  = []byte("this is a native/raw websocket message")
		events                         = neffos.Events{
			// Runs on the connection's own goroutine (triggered by an
			// incoming native message), so it must use t.Error, not t.Fatal.
			neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()

				expectedMessage := neffos.Message{
					Event:    neffos.OnNativeMessage,
					Body:     nativeMessage,
					IsNative: true,
				}

				if !reflect.DeepEqual(expectedMessage, msg) {
					t.Errorf("expected a native message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
				}

				return nil
			},
		}
	)

	serverHandler := neffos.JoinConnHandlers(neffos.Namespaces{namespace: events},
		neffos.Events{
			eventThatWillGiveErrorByServer: func(c *neffos.NSConn, msg neffos.Message) error {
				return errors.New(eventErrorText)
			},
		})
	ts := newTestServers(t, serverHandler)

	clientHandler := neffos.JoinConnHandlers(neffos.Namespaces{namespace: events},
		neffos.Events{
			// Runs on the client connection's own goroutine (triggered by the
			// server's error reply), so it must use t.Error, not t.Fatal.
			eventThatWillGiveErrorByServer: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()
				if !c.Conn.IsClient() {
					t.Errorf("this should only be executed by client-side, if not then the JoinConnHandlers didn't work as expected")
				}

				if msg.Err == nil {
					t.Errorf("expected an error from event: %s", eventThatWillGiveErrorByServer)
				} else if expected, got := eventErrorText, msg.Err.Error(); expected != got {
					t.Errorf("expected an error from event: %s to match: '%s' but got: '%s'", eventThatWillGiveErrorByServer, expected, got)
				}
				return nil
			},
		})

	ts.dial(t, clientHandler, func(backend string, client *neffos.Client) {
		defer client.Close()

		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}

		// Ask is not available on native websocket messages of course.
		wg.Add(1)
		c.Conn.Write(neffos.Message{
			Body:     nativeMessage,
			IsNative: true,
		})

		wg.Add(1)
		c.Emit(eventThatWillGiveErrorByServer, []byte("doesn't matter"))

		wg.Wait()
	})
}

func TestOnNativeMessageOnly(t *testing.T) {
	// when the only one namespace is "" and event OnNativeMessage.
	var (
		wg            sync.WaitGroup
		namespace     = ""
		nativeMessage = []byte("this is a native/raw websocket message")
		events        = neffos.Events{
			// Runs on the connection's own goroutine, so it must use t.Error,
			// not t.Fatal.
			neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()

				expectedMessage := neffos.Message{
					Event:    neffos.OnNativeMessage,
					Body:     nativeMessage,
					IsNative: true,
				}

				if !reflect.DeepEqual(expectedMessage, msg) {
					t.Errorf("expected a native message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
				}

				return nil
			},
		}
	)

	ts := newTestServers(t, events)

	ts.dial(t, events, func(backend string, client *neffos.Client) {
		defer client.Close()

		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}

		wg.Add(1)
		c.Conn.Write(neffos.Message{
			Body:     nativeMessage,
			IsNative: true,
		})

		wg.Wait()
	})
}

type objectTestRequest struct {
	Name string `json:"name"`
}

type objectTestReply struct {
	Greeting string   `json:"greeting"`
	Tags     []string `json:"tags"`
}

func TestObjectHelpers(t *testing.T) {
	const namespace = "default"

	received := make(chan string, 2) // bodies seen by the server on "note" and the room event.

	ts := newTestServers(t, neffos.Namespaces{namespace: neffos.Events{
		"greet": func(c *neffos.NSConn, msg neffos.Message) error {
			req, err := msg.As[objectTestRequest]()
			if err != nil {
				return err
			}
			return neffos.ReplyObject(objectTestReply{Greeting: "hello " + req.Name})
		},
		"note": func(c *neffos.NSConn, msg neffos.Message) error {
			received <- msg.Room + ":" + string(msg.Body)
			return nil
		},
	}})

	ts.dial(t, neffos.Namespaces{namespace: neffos.Events{}}, func(backend string, client *neffos.Client) {
		defer client.Close()

		c, err := client.Connect(context.TODO(), namespace)
		if err != nil {
			t.Fatal(err)
		}

		reply, err := c.AskObject[objectTestReply](context.TODO(), "greet", objectTestRequest{Name: "makis"})
		if err != nil {
			t.Fatalf("[%s] AskObject: %v", backend, err)
		}
		if reply.Greeting != "hello makis" {
			t.Fatalf("[%s] unexpected reply %+v", backend, reply)
		}
		if reply.Tags == nil {
			t.Fatalf("[%s] a nil slice must arrive as an empty JSON array, not null", backend)
		}

		if err := c.SendObject("note", objectTestRequest{Name: "ns"}); err != nil {
			t.Fatalf("[%s] SendObject: %v", backend, err)
		}
		if got, want := <-received, `:{"name":"ns"}`; got != want {
			t.Fatalf("[%s] NSConn.SendObject body: got %q want %q", backend, got, want)
		}

		room, err := c.JoinRoom(context.TODO(), "lobby")
		if err != nil {
			t.Fatal(err)
		}
		if err := room.SendObject("note", objectTestRequest{Name: "room"}); err != nil {
			t.Fatalf("[%s] Room.SendObject: %v", backend, err)
		}
		if got, want := <-received, `lobby:{"name":"room"}`; got != want {
			t.Fatalf("[%s] Room.SendObject body: got %q want %q", backend, got, want)
		}

		if err := c.SendObject("note", make(chan int)); err == nil {
			t.Fatalf("[%s] SendObject must report an unencodable value", backend)
		}
		if _, err := c.AskObject[objectTestReply](context.TODO(), "greet", make(chan int)); err == nil {
			t.Fatalf("[%s] AskObject must report an unencodable value", backend)
		}
	})
}
