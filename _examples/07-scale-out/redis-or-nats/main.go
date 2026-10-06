// Scale out: several neffos servers that act as one, through Redis or NATS.
//
// One server keeps its connections in memory, so a broadcast on server A
// never reaches a client of server B. A StackExchange fixes that: after
// Server.UseStackExchange, Broadcast publishes to Redis or NATS, and every
// server delivers what it receives to its own connections. Message.To works
// across servers too, so a private message finds bob wherever he is
// connected. The code that handles events does not change; only the setup
// does, which is two lines below.
//
// -exchange picks redis (github.com/kataras/neffos/stackexchange/redis) or
// nats (github.com/kataras/neffos/stackexchange/nats); REDIS_ADDR and
// NATS_URL point at them. Every server serves the chat page at / and the
// websocket at /ws. compose.yaml starts Redis and two servers, on 8080 and
// 9090; the Dockerfile builds from the repository root.
//
// Learn: connect several neffos servers with a Redis or NATS StackExchange.
//
// Requires: a Redis or NATS server, or Docker for compose.yaml.
//
// Run:
//
//	go run main.go -addr :8080 server                   # terminal 1, Redis on localhost:6379
//	go run main.go -addr :9090 server                   # terminal 2
//	go run main.go -addr :9090 -exchange nats server    # NATS instead, on localhost:4222
//	docker compose up --build                           # or both servers and Redis at once
//
// Try:
//
//	go run main.go -addr :8080 client alice    # then: hello
//	go run main.go -addr :9090 client bob      # prints "alice: hello" from the other server
//	open http://localhost:8080 and http://localhost:9090 in two tabs and chat between them
//	> /msg bob psst                            # alice's terminal; reaches bob on the other server
//
// Next: ../custom-stackexchange writes an exchange of your own.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
	"github.com/kataras/neffos/stackexchange/nats"
	"github.com/kataras/neffos/stackexchange/redis"
)

//go:embed index.html
var page []byte

const namespace = "chat"

var serverEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] joined", c.Conn.ID())
			c.BroadcastOthers(neffos.Message{Namespace: namespace, Event: "Notice", Body: []byte(c.Conn.ID() + " joined")})
			return nil
		},
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			msg.Body = []byte(c.Conn.ID() + ": " + string(msg.Body))
			c.BroadcastOthers(msg) // through the exchange: every server delivers it
			return nil
		},
		// "Private" carries "<user> <text>".
		"Private": func(c *neffos.NSConn, msg neffos.Message) error {
			to, text, ok := strings.Cut(string(msg.Body), " ")
			if !ok || to == "" {
				return errors.New(`expected "<user> <text>"`)
			}
			c.Conn.Server().Broadcast(nil, neffos.Message{
				To:        to, // found on whichever server holds that connection
				Namespace: namespace,
				Event:     "Private",
				Body:      []byte(c.Conn.ID() + ": " + text),
			})
			return nil
		},
	},
}

func main() {
	addr := flag.String("addr", ":8080", "the address to listen on, or the server to dial")
	exchange := flag.String("exchange", "redis", "redis or nats")
	flag.Parse()

	switch flag.Arg(0) {
	case "server":
		runServer(*addr, *exchange)
	case "client":
		if flag.NArg() < 2 {
			usage()
		}
		host := *addr
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		runClient("ws://"+host+"/ws", flag.Arg(1))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go [-addr :8080] [-exchange redis|nats] server | client <name>")
	os.Exit(2)
}

func newExchange(kind string) (neffos.StackExchange, error) {
	switch kind {
	case "redis":
		return redis.NewStackExchange(redis.Config{Addr: os.Getenv("REDIS_ADDR")}, "lobby")
	case "nats":
		url := os.Getenv("NATS_URL")
		if url == "" {
			url = "nats://localhost:4222"
		}
		return nats.NewStackExchange(url)
	default:
		return nil, fmt.Errorf("unknown exchange %q, expected redis or nats", kind)
	}
}

func runServer(addr, exchange string) {
	exc, err := newExchange(exchange)
	if err != nil {
		log.Fatal(err)
	}

	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	if err := srv.UseStackExchange(exc); err != nil {
		log.Fatal(err)
	}
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if name := r.Header.Get("X-Username"); name != "" {
			return name
		}
		if name := r.URL.Query().Get("name"); name != "" {
			return name
		}
		return neffos.DefaultIDGenerator(w, r)
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})

	log.Printf("listening on %s with %s, websocket endpoint /ws", addr, exchange)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(url, name string) {
	events := neffos.Namespaces{
		namespace: neffos.Events{
			"Chat":   show("%s\n"),
			"Notice": show("* %s\n"),
			"Private": func(c *neffos.NSConn, msg neffos.Message) error {
				if msg.Err != nil {
					fmt.Printf("not sent: %v\n", msg.Err)
					return nil
				}
				fmt.Printf("(private) %s\n", msg.Body)
				return nil
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": {name}})
	client, err := neffos.Dial(ctx, dialer, url, events)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	c, err := client.Connect(ctx, namespace)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if rest, ok := strings.CutPrefix(line, "/msg "); ok {
			c.Emit("Private", []byte(rest))
		} else if line != "" {
			c.Emit("Chat", []byte(line))
		}
	}
}

func show(format string) neffos.MessageHandlerFunc {
	return func(c *neffos.NSConn, msg neffos.Message) error {
		fmt.Printf(format, msg.Body)
		return nil
	}
}
