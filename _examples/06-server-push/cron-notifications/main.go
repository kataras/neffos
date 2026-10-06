// Scheduled pushes: a cron job that delivers pending notifications.
//
// Notifications pile up in a store while their users are away, through
// POST /notifications here, a queue or a database in a real app. A
// github.com/robfig/cron job runs every ten seconds, picks the pending
// notifications of the users who are online and pushes each one with
// Server.Broadcast and Message.To set to the user ID, which reaches that
// user's connection and nobody else. Server.IDGenerator makes the connection
// ID the user ID, read from the X-User header or ?user=, so no lookup table
// of connections is needed: GetConnections already says who is online.
//
// Broadcast does not confirm delivery; a notification counts as sent once it
// is handed to an online connection. Use Server.Ask where you need an answer.
//
// Learn: push from a scheduled job to specific users with Message.To.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2; within ten seconds prints alice's two waiting notifications
//
// Try:
//
//	curl -i -d '{"user":"alice","text":"your order shipped"}' localhost:8080/notifications    # 202; alice prints it at the next run
//	curl -i -d '{"user":"","text":"x"}' localhost:8080/notifications                          # 400 Bad Request
//
// Next: ../../07-scale-out/redis-or-nats runs the same server on several machines.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"

	"github.com/robfig/cron/v3"
)

const namespace = "agent"

type notification struct {
	User string `json:"user"`
	Text string `json:"text"`
}

// store keeps the notifications that were not delivered yet, per user.
type store struct {
	mu      sync.Mutex
	pending map[string][]notification
}

func (s *store) add(n notification) {
	s.mu.Lock()
	s.pending[n.User] = append(s.pending[n.User], n)
	s.mu.Unlock()
}

// take removes and returns the pending notifications of the given users.
func (s *store) take(users map[string]*neffos.Conn) []notification {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []notification
	for user := range users {
		out = append(out, s.pending[user]...)
		delete(s.pending, user)
	}
	return out
}

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
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <user>")
	os.Exit(2)
}

func runServer(addr string) {
	notifications := &store{pending: map[string][]notification{
		"alice": {{"alice", "welcome back"}, {"alice", "you have a new follower"}},
		"bob":   {{"bob", "your invoice is ready"}},
	}}

	srv := neffos.New(gorilla.DefaultUpgrader, neffos.Namespaces{namespace: neffos.Events{}})
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if user := r.Header.Get("X-User"); user != "" {
			return user
		}
		if user := r.URL.Query().Get("user"); user != "" {
			return user
		}
		return neffos.DefaultIDGenerator(w, r)
	}

	jobs := cron.New()
	jobs.AddFunc("@every 10s", func() {
		due := notifications.take(srv.GetConnections())
		for _, n := range due {
			body, err := neffos.Marshal(n)
			if err != nil {
				log.Printf("cron: cannot encode the notification for %s: %v", n.User, err)
				continue
			}
			srv.Broadcast(nil, neffos.Message{
				To:        n.User, // only this user's connection
				Namespace: namespace,
				Event:     "Notification",
				Body:      body,
			})
		}
		log.Printf("cron: pushed %d notification(s)", len(due))
	})
	jobs.Start()
	defer jobs.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	mux.HandleFunc("POST /notifications", func(w http.ResponseWriter, r *http.Request) {
		var n notification
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil || n.User == "" || n.Text == "" {
			http.Error(w, "expected {\"user\":\"...\",\"text\":\"...\"}", http.StatusBadRequest)
			return
		}
		notifications.add(n)
		w.WriteHeader(http.StatusAccepted)
	})

	log.Printf("listening on %s, websocket endpoint /ws, POST /notifications", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(addr, user string) {
	events := neffos.Namespaces{
		namespace: neffos.Events{
			"Notification": func(c *neffos.NSConn, msg neffos.Message) error {
				n, err := msg.As[notification]()
				if err != nil {
					return err
				}
				fmt.Printf("notification: %s\n", n.Text)
				return nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"X-User": {user}})
	client, err := neffos.Dial(ctx, dialer, addr, events)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Connect(ctx, namespace); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("waiting for %s's notifications, Ctrl+C to stop\n", user)
	<-client.NotifyClose
}
