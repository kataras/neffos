package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kataras/fastws/_examples/advanced/ws"
	"github.com/kataras/fastws/_examples/advanced/ws/gobwas"
	"github.com/kataras/fastws/_examples/advanced/ws/gorilla"
)

const (
	endpoint  = "localhost:9090"
	namespace = "default"
	timeout   = 8 * time.Second
)

var handler = ws.WithTimeout{
	ReadTimeout:  timeout,
	WriteTimeout: timeout,
	Namespaces: ws.Namespaces{
		"default": ws.Events{
			ws.OnNamespaceConnect: func(c ws.NSConn, msg ws.Message) error {
				if msg.Err != nil {
					log.Printf("This client can't connect because of: %v", msg.Err)
					return nil
				}

				err := fmt.Errorf("Server says that you are not allowed here")
				/* comment this to see that the server-side will
				no allow to for this socket to be connected to the "default" namespace
				and an error will be logged to the client. */
				err = nil

				return err
			},
			ws.OnNamespaceConnected: func(c ws.NSConn, msg ws.Message) error {
				if !c.Conn().IsClient() {
					c.Emit("chat", []byte("welcome to server's namespace"))
				}

				log.Printf("[%s] connected to [%s].", c.Conn().ID(), msg.Namespace)

				return nil
			},
			ws.OnNamespaceDisconnect: func(c ws.NSConn, msg ws.Message) error {
				if msg.Err != nil {
					log.Printf("This client can't disconnect yet, server does not allow that action, reason: %v", msg.Err)
					return nil
				}

				err := fmt.Errorf("Server says that you are not allowed to be disconnected yet")
				/* here if you comment this, the return error will mean that
				the disconnect message from client-side will be ignored from the server
				and the connection would be still available to send message to the "default" namespace
				it will not be disconnected.*/
				err = nil

				if err == nil {
					log.Printf("[%s] disconnected from [%s].", c.Conn().ID(), msg.Namespace)
				}

				if c.Conn().IsClient() {
					os.Exit(0)
				}

				return err
			},
			"chat": func(c ws.NSConn, msg ws.Message) error {
				if !c.Conn().IsClient() {
					// this is possible too:
					// if bytes.Equal(msg.Body, []byte("force disconnect")) {
					// 	println("force disconnect")
					// 	return c.Disconnect()
					// }

					log.Printf("--server-side-- send back the message [%s:%s]", msg.Event, string(msg.Body))
					//	c.Emit(msg.Event, msg.Body)
					//	c.Server().Broadcast(nil, msg) // to all including this connection.
					c.Conn().Server().Broadcast(c.Conn(), msg) // to all except this connection.
				}

				log.Printf("---------------------\n[%s] %s", c.Conn().ID(), msg.Body)
				return nil
			},
		},
	},
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	var (
		upgrader = gobwas.DefaultUpgrader
		dialer   = gobwas.DefaultDialer
	)

	if len(args) > 1 {
		method := args[1]
		if method == "gorilla" {
			upgrader = gorilla.DefaultUpgrader
			dialer = gorilla.DefaultDialer
			if side == "server" {
				log.Printf("Using with Gorilla Upgrader.")
			} else {
				log.Printf("Using with Gorilla Dialer.")
			}
		}
	}

	switch side {
	case "server":
		server(upgrader)
	case "client":
		client(dialer)
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func server(upgrader ws.Upgrader) {
	srv := ws.New(upgrader, handler)

	srv.OnConnect = func(c ws.Conn) error {
		log.Printf("[%s] connected to server.", c.ID())
		// time.Sleep(3 * time.Second)
		// c.Connect(nil, namespace) // auto-connect to a specific namespace.
		// c.Write(namespace, "chat", []byte("Welcome to the server (after namespace connect)"))
		// println("client connected")
		return nil
	}
	srv.OnDisconnect = func(c ws.Conn) {
		log.Printf("[%s] disconnected from the server.", c.ID())
	}
	srv.OnError = func(c ws.Conn, err error) bool {
		log.Printf("ERROR: [%s] %v", c.ID(), err)
		return false
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	go http.ListenAndServe(endpoint, srv)

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()
		if bytes.Equal(text, []byte("force disconnect")) {
			// for _, conn := range srv.GetConnectionsByNamespace(namespace) {
			// 	conn.Disconnect()
			// }
			// srv.Broadcast(nil, ws.Message{
			// 	Namespace: namespace,
			// 	Event:     ws.OnNamespaceDisconnect,
			// })
			srv.Do(func(c ws.Conn) {
				// c.Close()
				c.Namespace(namespace).Disconnect(nil)
			})
		} else {
			// srv.Do(func(c ws.Conn) {
			// 	c.Write(namespace, "chat", text)
			// })
			srv.Broadcast(nil, ws.Message{Namespace: namespace, Event: "chat", Body: text})
		}
		fmt.Fprint(os.Stdout, ">> ")
	}
}

func client(dialer ws.Dialer) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(timeout))
	defer cancel()

	client, err := ws.Dial(dialer, ctx, endpoint, handler)
	if err != nil {
		panic(err)
	}

	defer client.Close()

	// connectNamespaceTimeout, cancel2 := context.WithTimeout(context.Background(), timeout/2)
	// defer cancel2()

	// c, err := client.WaitServerConnect(nil, namespace)
	c, err := client.Connect(nil, namespace)
	if err != nil {
		panic(err)
	}
	// println("connected.")

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()

		if bytes.Equal(text, []byte("exit")) {
			if err := c.Disconnect(nil); err != nil {
				// log.Printf("from server: %v", err)
			}
			continue
		}

		ok := c.Emit("chat", text)
		if !ok {
			break
		}

		fmt.Fprint(os.Stdout, ">> ")
	}

}
