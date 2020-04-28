package main

import (
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"

	"github.com/robfig/cron/v3"
)

/*
	stress-test broadcasting every 2seconds (10 times, 10000 messages)
	to 1000 clients with the same ID.
*/

const (
	namespace    = "agent"
	clientEvent  = "notify"
	serverEvent  = "ackNotify"
	clientConnID = "client"
	every        = "2s"

	clientsCount  = 1000
	messagesCount = clientsCount * 10
)

type notification struct {
	Title   string `json:"title"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

var message = neffos.Message{
	To:        clientConnID,
	Namespace: namespace,
	Event:     clientEvent,
	Body: neffos.Marshal(notification{
		Title:   "master notification",
		Subject: "master",
		Message: "a notification message",
	}),
}

var (
	serverEvents = neffos.WithTimeout{
		Namespaces: neffos.Namespaces{
			namespace: neffos.Events{
				serverEvent: func(ns *neffos.NSConn, msg neffos.Message) error {
					return nil
				},
			},
		},
	}

	received uint32

	clientEvents = neffos.Namespaces{
		namespace: neffos.Events{
			clientEvent: func(ns *neffos.NSConn, msg neffos.Message) error {
				n := atomic.AddUint32(&received, 1)
				log.Printf("Total Received: %d", n)
				return nil
			},
		},
	}
)

func main() {
	// connect all and then start cron.
	server := startServer()
	time.Sleep(200 * time.Millisecond)

	wg := new(sync.WaitGroup)
	wg.Add(clientsCount)
	wgClose := new(sync.WaitGroup)
	wgClose.Add(clientsCount)

	now := time.Now()

	for i := 0; i < clientsCount; i++ {
		go startClient(wg, wgClose)
	}

	wg.Wait()
	startCron(server)

	wgClose.Wait()
	log.Printf("Done in: %s", time.Since(now))
}

func startServer() *neffos.Server {
	server := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	server.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		username := r.Header.Get("X-Username")
		if username == "" {
			log.Fatalf("expected a username but got nothing")
		}

		return username
	}
	server.SyncBroadcaster = true

	go func() {
		log.Fatal(http.ListenAndServe(":8080", server))
	}()

	return server
}

func startCron(server *neffos.Server) {
	c := cron.New()
	_, err := c.AddFunc("@every "+every, func() {
		if atomic.LoadUint32(&received) == messagesCount {
			c.Stop()
			server.Close()
			return
		}

		pushNotifications(server)
	})

	if err != nil {
		log.Fatal(err)
	}

	c.Start()
}

func pushNotifications(server *neffos.Server) {
	server.Broadcast(nil, message)
}

var dialer = gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": []string{clientConnID}})

var connectedN uint32

func startClient(wg *sync.WaitGroup, wgClose *sync.WaitGroup) {
	client, err := neffos.Dial(nil, dialer, "ws://localhost:8080", clientEvents)
	if err != nil {
		log.Fatal(err)
	}

	_, err = client.Connect(nil, namespace)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Total Connected: %d", atomic.AddUint32(&connectedN, 1))
	wg.Done()

	<-client.NotifyClose

	wgClose.Done()
}
