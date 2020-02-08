package neffos_test

import (
	"bytes"
	"context"
	"fmt"
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

	teardownServer := runTestServer("localhost:8080", neffos.Namespaces{
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
	defer teardownServer()

	err := runTestClient("localhost:8080", neffos.Namespaces{
		"":           emptyEvents,
		namespace:    emptyEvents,
		onlyOnClient: emptyEvents,
		namespaceThatShouldErrOnServer: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				t.Fatalf("%s namespace shouldn't be accessible to the client to connect", namespaceThatShouldErrOnServer)
				return nil
			},
		},
		namespaceThatShouldErrOnClient: neffos.Events{
			neffos.OnNamespaceConnect: func(c *neffos.NSConn, msg neffos.Message) error {
				return neffos.ErrBadNamespace
			},
		},
	},
		func(dialer string, client *neffos.Client) {
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

		})()
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

	teardownServer := runTestServer("localhost:8080", neffos.Namespaces{namespace: neffos.Events{
		pingEvent: func(c *neffos.NSConn, msg neffos.Message) error {
			// c.Emit("event", pongMessage)
			return neffos.Reply(pongMessage) // changes only body; ns,event remains.
		}}})
	defer teardownServer()

	err := runTestClient("localhost:8080", neffos.Namespaces{namespace: neffos.Events{}}, func(dialer string, client *neffos.Client) {
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
			testMessage(dialer, i, msg)
		}

		msg, err := c.Ask(context.TODO(), pingEvent, nil)
		if err != nil {
			t.Fatal(err)
		}
		testMessage(dialer, -1, msg)
	})()
	if err != nil {
		t.Fatal(err)
	}
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

				t.Fatalf("expected message to be:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
			}
		}
	)

	teardownServer := runTestServer("localhost:8080", neffos.Namespaces{namespace: neffos.Events{
		neffos.OnAnyEvent: func(c *neffos.NSConn, msg neffos.Message) error {
			if neffos.IsSystemEvent(msg.Event) { // skip connect/disconnect messages.
				return nil
			}

			return neffos.Reply(msg.Body)
		}}})
	defer teardownServer()

	err := runTestClient("localhost:8080", neffos.Namespaces{namespace: neffos.Events{
		expectedMessage.Event: func(c *neffos.NSConn, msg neffos.Message) error {
			defer wg.Done()
			testMessage(msg)

			return nil
		},
	}}, func(dialer string, client *neffos.Client) {
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
	})()
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnNativeMessageAndMessageError(t *testing.T) {
	var (
		wg                             sync.WaitGroup
		namespace                      = "" // empty namespace and OnNativeMessage event defined to allow native websocket messages to come through.
		eventThatWillGiveErrorByServer = "event_error_server"
		eventErrorText                 = "this event will give error by server"
		nativeMessage                  = []byte("this is a native/raw websocket message")
		events                         = neffos.Events{
			neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()

				expectedMessage := neffos.Message{
					Event:    neffos.OnNativeMessage,
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

	serverHandler := neffos.JoinConnHandlers(neffos.Namespaces{namespace: events},
		neffos.Events{
			eventThatWillGiveErrorByServer: func(c *neffos.NSConn, msg neffos.Message) error {
				return fmt.Errorf(eventErrorText)
			},
		})
	teardownServer := runTestServer("localhost:8080", serverHandler)
	defer teardownServer()

	clientHandler := neffos.JoinConnHandlers(neffos.Namespaces{namespace: events},
		neffos.Events{
			eventThatWillGiveErrorByServer: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()
				if !c.Conn.IsClient() {
					t.Fatalf("this should only be executed by client-side, if not then the JoinConnHandlers didn't work as expected")
				}

				if msg.Err == nil {
					t.Fatalf("expected an error from event: %s", eventThatWillGiveErrorByServer)
				}
				if expected, got := eventErrorText, msg.Err.Error(); expected != got {
					t.Fatalf("expected an error from event: %s to match: '%s' but got: '%s'", eventThatWillGiveErrorByServer, expected, got)
				}
				return nil
			},
		})

	err := runTestClient("localhost:8080", clientHandler, func(dialer string, client *neffos.Client) {
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
	})()
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnNativeMessageOnly(t *testing.T) {
	// when the only one namespace is "" and event OnNativeMessage.
	var (
		wg            sync.WaitGroup
		namespace     = ""
		nativeMessage = []byte("this is a native/raw websocket message")
		events        = neffos.Events{
			neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
				defer wg.Done()

				expectedMessage := neffos.Message{
					Event:    neffos.OnNativeMessage,
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

	teardownServer := runTestServer("localhost:8080", events)
	defer teardownServer()

	err := runTestClient("localhost:8080", events, func(dialer string, client *neffos.Client) {
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
	})()
	if err != nil {
		t.Fatal(err)
	}
}

// No need to encourage users to use go routines for event sending even if it's totally safe in neffos.
// It works but ^
// func TestSimultaneouslyEventsRoutines(t *testing.T) {
// 	// test multiple goroutines sending events, it should work because the lib it is designed to take care of these things.
// 	var (
// 		wg               sync.WaitGroup
// 		namespace        = "namespace1"
// 		event1           = "event1"
// 		event2           = "event2"
// 		event3           = "event3"
// 		expectedMessages = map[string]neffos.Message{
// 			event1: {
// 				Namespace: namespace,
// 				Event:     event1,
// 				Body:      []byte("body1"),
// 			},
// 			event2: {
// 				Namespace: namespace,
// 				Event:     event2,
// 				Body:      []byte("body2"),
// 			},
// 			event3: {
// 				Namespace: namespace,
// 				Event:     event3,
// 				Body:      []byte("body3"),
// 			},
// 		}
// 		events = neffos.Events{
// 			neffos.OnAnyEvent: func(c *neffos.NSConn, msg neffos.Message) error {
// 				if neffos.IsSystemEvent(msg.Event) {
// 					return nil
// 				}

// 				expectedMessage := expectedMessages[msg.Event]

// 				if !reflect.DeepEqual(msg, expectedMessage) {
// 					t.Fatalf("expected message:\n%#+v\n\tbut got:\n%#+v", expectedMessage, msg)
// 				}

// 				if c.Conn.IsClient() {
// 					// wait for server's reply to the client's send act before done with this event test.
// 					defer wg.Done()
// 				} else {
// 					// send back to the client the message as it's.
// 					return neffos.Reply(msg.Body)
// 				}

// 				return nil
// 			},
// 		}
// 	)

// 	teardownServer := runTestServer("localhost:8080", neffos.Namespaces{namespace: events})
// 	defer teardownServer()

// 	err := runTestClient("localhost:8080", neffos.Namespaces{namespace: events},
// 		func(dialer string, client *neffos.Client) {
// 			defer client.Close()

// 			c, err := client.Connect(nil, namespace)
// 			if err != nil {
// 				t.Fatal(err)
// 			}

// 			send := func(event string) {
// 				wg.Add(1)
// 				msg := expectedMessages[event]
// 				c.Emit(msg.Event, msg.Body)
// 			}

// 			for i := 0; i < 5; i++ {
// 				go send(event1)
// 				go send(event2)
// 				go send(event3)
// 			}

// 			for i := 0; i < 5; i++ {
// 				go send(event3)
// 				go send(event2)
// 				go send(event1)
// 			}

// 			wg.Wait()
// 		})()
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// }
