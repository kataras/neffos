// Pushing to websocket clients from an ordinary HTTP handler.
//
// Most pushes start outside the websocket: a payment webhook, an admin
// form, another service. The *neffos.Server is a value like any other, so an
// HTTP handler can hold it and call Server.Broadcast. With nil as the first
// argument nobody is excluded; Message.To set to a connection ID narrows the
// broadcast to that connection, and left empty it reaches everyone in the
// namespace. Here POST /notify?user=alice sends the request body to alice,
// and POST /notify without a user sends it to everyone. The handler checks
// Server.GetConnections first, so it can answer 404 for a user who is not
// online instead of dropping the message silently.
//
// Learn: send websocket messages from HTTP handlers with Server.Broadcast and Message.To.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	curl -i -d "your order shipped" "localhost:8080/notify?user=alice"    # 202 Accepted; only alice prints "notification: your order shipped"
//	curl -i -d "maintenance at five" localhost:8080/notify                # 202 Accepted; both print it
//	curl -i -d "hi" "localhost:8080/notify?user=carol"                    # 404 Not Found: carol is not online
//
// Next: ../cron-notifications pushes from a scheduled job instead.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const namespace = "notifications"

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "server":
		runServer(":8080")
	case "client":
		if len(os.Args) < 3 {
			usage()
		}
		runClient("ws://localhost:8080/ws", os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name>")
	os.Exit(2)
}

func runServer(addr string) {
	srv := neffos.New(gorilla.DefaultUpgrader, neffos.Namespaces{namespace: neffos.Events{}})
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("user")
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	mux.Handle("POST /notify", notify(srv))

	log.Printf("listening on %s, websocket endpoint /ws, POST /notify", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// notify sends the request body to ?user=, or to everyone without it.
func notify(srv *neffos.Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<10))
		if err != nil || len(body) == 0 {
			http.Error(w, "the body is the notification text", http.StatusBadRequest)
			return
		}

		user := r.URL.Query().Get("user")
		if _, online := srv.GetConnections()[user]; user != "" && !online {
			http.Error(w, user+" is not online", http.StatusNotFound)
			return
		}

		srv.Broadcast(nil, neffos.Message{
			To:        user, // empty: every connection in the namespace
			Namespace: namespace,
			Event:     "Notify",
			Body:      body,
		})
		log.Printf("notified %q: %s", user, body)
		w.WriteHeader(http.StatusAccepted)
	})
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?user="+name, neffos.Namespaces{
		namespace: neffos.Events{
			"Notify": func(c *neffos.NSConn, msg neffos.Message) error {
				fmt.Printf("notification: %s\n", msg.Body)
				return nil
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Connect(ctx, namespace); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("waiting for %s's notifications, Ctrl+C to stop\n", name)

	// This client only listens: wait for Ctrl+C or for the server to close.
	stop, cancelStop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancelStop()
	select {
	case <-stop.Done():
	case <-client.NotifyClose:
		fmt.Println("connection closed")
	}
}
