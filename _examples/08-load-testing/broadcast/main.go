// Load test: one broadcast, many receivers.
//
// One program runs both sides. It starts a server on :9595, connects
// clientsCount Go clients that all share the connection ID "client" (the
// IDGenerator reads it from X-Username), then a time.Ticker calls
// Server.Broadcast every two seconds with Message.To set to that ID, so
// every client receives every round. Broadcast does not wait for the
// receivers: each connection writes the rounds in order, at its own pace.
// Once the clients have counted rounds*clientsCount messages, the server
// closes, the clients see NotifyClose, and the program prints how long it
// took.
//
// Learn: measure Server.Broadcast against many connections in one process.
//
// Run:
//
//	go run main.go
//
// Try:
//
//	# the log counts connected clients, then received messages, then prints "done in ..."
//
// Next: ../../09-integrations/iris-jwt mounts neffos on an Iris application.
package main

import (
	"context"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const (
	addr         = ":9595"
	namespace    = "agent"
	clientConnID = "client"
	every        = 2 * time.Second

	clientsCount  = 1000
	rounds        = 10
	messagesCount = clientsCount * rounds
)

type notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

var message = neffos.Message{
	To:        clientConnID,
	Namespace: namespace,
	Event:     "notify",
	Body:      neffos.Marshal(notification{Title: "load test", Message: "a notification message"}),
}

var (
	received   atomic.Uint32
	connectedN atomic.Uint32

	clientEvents = neffos.Namespaces{
		namespace: neffos.Events{
			"notify": func(ns *neffos.NSConn, msg neffos.Message) error {
				if n := received.Add(1); n%clientsCount == 0 {
					log.Printf("received: %d", n)
				}
				return nil
			},
		},
	}
)

func main() {
	server := startServer()
	time.Sleep(200 * time.Millisecond)

	var connected, closed sync.WaitGroup
	connected.Add(clientsCount)
	closed.Add(clientsCount)

	start := time.Now()
	for range clientsCount {
		go startClient(&connected, &closed)
	}

	connected.Wait()
	go pushEvery(server, every)

	closed.Wait()
	log.Printf("done in %s", time.Since(start))
}

func startServer() *neffos.Server {
	server := neffos.New(gorilla.DefaultUpgrader, neffos.Namespaces{namespace: neffos.Events{}})
	server.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.Header.Get("X-Username")
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	go func() {
		log.Fatal(http.ListenAndServe(addr, mux))
	}()

	return server
}

// pushEvery broadcasts one round per tick until every message has arrived.
func pushEvery(server *neffos.Server, d time.Duration) {
	ticker := time.NewTicker(d)
	defer ticker.Stop()

	for range ticker.C {
		if received.Load() >= messagesCount {
			server.Close()
			return
		}
		server.Broadcast(nil, message)
	}
}

var dialer = gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": {clientConnID}})

// dialing limits the handshakes in flight, so the listen backlog never overflows
// (on Windows it holds about 200 pending connections).
var dialing = make(chan struct{}, 100)

func startClient(connected, closed *sync.WaitGroup) {
	defer closed.Done()

	dialing <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, dialer, "ws://localhost"+addr+"/ws", clientEvents)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := client.Connect(ctx, namespace); err != nil {
		log.Fatal(err)
	}
	<-dialing

	if n := connectedN.Add(1); n%100 == 0 {
		log.Printf("connected: %d", n)
	}
	connected.Done()

	<-client.NotifyClose
}
