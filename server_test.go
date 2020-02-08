package neffos_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kataras/neffos"

	gobwas "github.com/kataras/neffos/gobwas"
	gorilla "github.com/kataras/neffos/gorilla"

	"golang.org/x/sync/errgroup"
)

func runTestServer(addr string, connHandler neffos.ConnHandler, configureServer ...func(*neffos.Server)) func() error {
	gobwasServer := neffos.New(gobwas.DefaultUpgrader, connHandler)
	gorillaServer := neffos.New(gorilla.DefaultUpgrader, connHandler)

	for _, cfg := range configureServer {
		cfg(gobwasServer)
		cfg(gorillaServer)
	}

	mux := http.NewServeMux()
	mux.Handle("/gobwas", gobwasServer)
	mux.Handle("/gorilla", gorillaServer)

	httpServer := http.Server{
		Addr:    addr,
		Handler: mux,
	}
	go httpServer.ListenAndServe()
	time.Sleep(200 * time.Millisecond)

	// teardown.
	return func() error {
		gorillaServer.Close()
		gobwasServer.Close()
		return httpServer.Close()
	}
}

func TestServerBroadcastTo(t *testing.T) {
	// we fire up two connections, one with the "conn_ID" and other with the default uuid id generator,
	// the message which the second client emits should only be sent to the connection with the ID of "conn_ID".

	var (
		wg        sync.WaitGroup
		namespace = "default"
		body      = []byte("data")
		to        = "conn_ID"
		events    = neffos.Namespaces{
			namespace: neffos.Events{
				"event": func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						if !bytes.Equal(msg.Body, body) {
							t.Fatalf("expected event's incoming data to be: %s but got: %s", string(body), string(msg.Body))
						}

						if c.String() != to {
							t.Fatalf("expected the message to be sent only to the connection with an ID of 'conn_ID'")
						}

						wg.Done()
					} else {
						msg.To = to
						c.Conn.Server().Broadcast(c, msg)
					}

					return nil
				},
			},
		}
	)

	teardownServer := runTestServer("localhost:8080", events, func(wsServer *neffos.Server) {
		once := new(uint32)
		wsServer.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
			if atomic.CompareAndSwapUint32(once, 0, 1) {
				return to // set the "to" only to the first conn for test.
			}

			return neffos.DefaultIDGenerator(w, r)
		}
	})
	defer teardownServer()

	wg.Add(2)

	teardownClient1 := runTestClient("localhost:8080", events,
		func(dialer string, client *neffos.Client) {
			_, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}

		})

	defer teardownClient1()

	teardownClient2 := runTestClient("localhost:8080", events,
		func(dialer string, client *neffos.Client) {
			c, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}
			c.Emit("event", body)
		})
	defer teardownClient2()

	wg.Wait()
}

func TestServerAsk(t *testing.T) {
	// we fire up two connections, one with the "conn_ID" and other with the default uuid id generator,
	// the message which the second client emits should only be sent to the connection with the ID of "conn_ID".

	var (
		wg             sync.WaitGroup
		namespace      = "default"
		body           = []byte("data")
		expectResponse = append(body, []byte("ok")...)
		to             = "conn_ID"
		clientEvents   = neffos.Namespaces{
			namespace: neffos.Events{
				"ask": func(c *neffos.NSConn, msg neffos.Message) error {
					return neffos.Reply(expectResponse)
				},
			},
		}
	)

	g := new(errgroup.Group)

	teardownServer := runTestServer("localhost:8080", neffos.Namespaces{namespace: neffos.Events{}}, func(wsServer *neffos.Server) {
		once := new(uint32)
		wsServer.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
			if atomic.CompareAndSwapUint32(once, 0, 1) {
				return to // set the "to" only to the first conn for test.
			}

			return neffos.DefaultIDGenerator(w, r)
		}

		wgWaitToAllConnect := new(sync.WaitGroup)
		wgWaitToAllConnect.Add(2)
		wsServer.OnConnect = func(c *neffos.Conn) error {
			wgWaitToAllConnect.Done()
			return nil
		}

		worker := func() error {
			wgWaitToAllConnect.Wait()

			response, err := wsServer.Ask(context.TODO(), neffos.Message{
				Namespace: "default",
				Event:     "ask",
				To:        to,
			})

			if err != nil {
				return err
			}

			if !bytes.Equal(response.Body, expectResponse) {
				return fmt.Errorf("expected response with body: %s but got: %s", string(expectResponse), string(response.Body))
			}

			wg.Done()
			return nil
		}

		g.Go(worker)
	})
	defer teardownServer()

	wg.Add(2) // two servers, a gorilla and gobwas.

	teardownClient1 := runTestClient("localhost:8080", clientEvents,
		func(dialer string, client *neffos.Client) {
			_, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}
		})

	defer teardownClient1()

	teardownClient2 := runTestClient("localhost:8080", clientEvents,
		func(dialer string, client *neffos.Client) {
			_, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}
		})
	defer teardownClient2()

	wg.Wait()
	if err := g.Wait(); err != nil {
		t.Fatal(err)
	}
}
