package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/ws"
	"github.com/kataras/ws/gobwas"
	"github.com/kataras/ws/gorilla"
)

const (
	endpoint  = "localhost:8080"
	namespace = "default"
	timeout   = 0 // 30 * time.Second
)

var handler = ws.WithTimeout{
	ReadTimeout:  timeout,
	WriteTimeout: timeout,
	Namespaces: ws.Namespaces{
		"default": ws.Events{
			ws.OnNamespaceConnect: func(c *ws.NSConn, msg ws.Message) error {
				return nil
			},
			ws.OnNamespaceConnected: func(c *ws.NSConn, msg ws.Message) error {
				log.Printf("[%s] connected to [%s].", c.Conn.ID(), msg.Namespace)

				c.Emit("chat", []byte("welcome to server's namespace"))

				return nil
			},
			ws.OnNamespaceDisconnect: func(c *ws.NSConn, msg ws.Message) error {
				log.Printf("[%s] disconnected from [%s].", c.Conn.ID(), msg.Namespace)
				return nil
			},
			"chat": func(c *ws.NSConn, msg ws.Message) error {
				log.Printf("--server-side-- send back the message [%s:%s]", msg.Event, string(msg.Body))
				//	c.Emit(msg.Event, msg.Body)
				//	c.Server().Broadcast(nil, msg) // to all including this connection.
				c.Conn.Server().Broadcast(c.Conn, msg) // to all except this connection.

				log.Printf("---------------------\n[%s] %s", c.Conn.ID(), msg.Body)
				return nil
			},
		},
	},
}

func main() {
	args := os.Args[1:]
	if len(args) > 1 {
		log.Fatalf("expected program to start with 'gobwas' or 'gorilla' argument")
	}
	upgrader := gobwas.DefaultUpgrader

	if len(args) > 0 {
		method := args[0]

		if method == "gorilla" {
			upgrader = gorilla.DefaultUpgrader
			log.Printf("Using with Gorilla Upgrader.")
		}
	}

	server(upgrader)
}

var (
	// tests immediately closed on the `Server#OnConnect`.
	dissalowAll = false
	// if not empty, tests broadcast on `Server#OnConnect` (expect this conn because it is not yet connected to any namespace locally).
	notifyOthers                  = true
	serverHandlesConnectNamespace = false
)

func server(upgrader ws.Upgrader) {
	srv := ws.New(upgrader, handler)

	srv.OnConnect = func(c *ws.Conn) error {
		if dissalowAll {
			return fmt.Errorf("you are not allowed to connect here for some reason")
		}

		log.Printf("[%s] connected to server.", c.ID())

		if serverHandlesConnectNamespace {
			ns, err := c.Connect(nil, namespace)
			if err != nil {
				panic(err)
			}

			ns.Emit("chat", []byte("(Force-connected by server)"))
		}

		if notifyOthers {
			c.Server().Broadcast(c, ws.Message{
				Namespace: namespace,
				Event:     "chat",
				Body:      []byte(fmt.Sprintf("Client [%s] connected too.", c.ID())),
			})
		}

		return nil
	}

	srv.OnDisconnect = func(c *ws.Conn) {
		log.Printf("[%s] disconnected from the server.", c.ID())
	}

	srv.OnUpgradeError = func(err error) {
		log.Printf("ERROR: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/echo", srv)
	mux.Handle("/", http.FileServer(http.Dir("./")))

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	go http.ListenAndServe(endpoint, mux)

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()
		if bytes.Equal(text, []byte("force disconnect")) {
			srv.Do(func(c *ws.Conn) {
				c.DisconnectAll(nil)
			})
		} else {
			srv.Broadcast(nil, ws.Message{Namespace: namespace, Event: "chat", Body: text})
		}
		fmt.Fprint(os.Stdout, ">> ")
	}
}
