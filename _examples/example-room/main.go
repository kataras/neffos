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

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

const (
	endpoint  = "localhost:9090"
	namespace = "default"
	timeout   = 60 * time.Second
)

var handler = neffos.WithTimeout{
	ReadTimeout:  timeout,
	WriteTimeout: timeout,
	Namespaces: neffos.Namespaces{
		"default": neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] connected to [%s].", c.Conn.ID(), msg.Namespace)

				if !c.Conn.IsClient() {
					c.Emit("chat", []byte("welcome to server's namespace"))
				}

				return nil
			},
			neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] disconnected from [%s].", c.Conn.ID(), msg.Namespace)

				if c.Conn.IsClient() {
					os.Exit(0)
				}

				return nil
			},
			neffos.OnRoomJoined: func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] joined to room [%s].", c.Conn.ID(), msg.Room)
				return nil
			},
			neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] left from room [%s].", c.Conn.ID(), msg.Room)
				return nil
			},
			"chat": func(c *neffos.NSConn, msg neffos.Message) error {
				if !c.Conn.IsClient() {
					log.Printf("--server-side-- send back the message [%s:%s]", msg.Event, string(msg.Body))

					if msg.Room == "" {
						// send back the message to the client.
						// c.Emit(msg.Event, msg.Body) or
						return neffos.Reply(msg.Body)
					}

					c.Conn.Server().Broadcast(c.Conn, neffos.Message{
						Namespace: msg.Namespace,
						Event:     msg.Event,
						// Broadcast to all other members inside this room except this connection(the emmiter, client in this case).
						// If first argument was nil then to all inside this room including this connection.
						Room: msg.Room,
						Body: msg.Body,
					})
				}

				log.Printf("---------------------\n[%s] %s", c.Conn.ID(), msg.Body)
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

func server(upgrader neffos.Upgrader) {
	srv := neffos.New(upgrader, handler)
	srv.OnConnect = func(c *neffos.Conn) error {
		log.Printf("[%s] connected to server.", c.ID())
		// time.Sleep(3 * time.Second)
		// c.Connect(nil, namespace) // auto-connect to a specific namespace.
		// c.Write(namespace, "chat", []byte("Welcome to the server (after namespace connect)"))
		// println("client connected")
		return nil
	}
	srv.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected from the server.", c.ID())
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("ERROR: %v", err)
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
			// srv.Broadcast(nil, neffos.Message{
			// 	Namespace: namespace,
			// 	Event:     neffos.OnNamespaceDisconnect,
			// })
			srv.Do(func(c *neffos.Conn) {
				// c.Close()
				c.Namespace(namespace).Disconnect(nil)
			}, false)
		} else {
			// srv.Do(func(c neffos.Conn) {
			// 	c.Write(namespace, "chat", text)
			// }, false)
			srv.Broadcast(nil, neffos.Message{Namespace: namespace, Event: "chat", Body: text})
		}
		fmt.Fprint(os.Stdout, ">> ")
	}
}

func client(dialer neffos.Dialer) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(timeout))
	defer cancel()

	client, err := neffos.Dial(ctx, dialer, endpoint, handler)
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

	var joinKeyword = []byte("join")
	var leaveKeyword = []byte("leave")

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
		} else if bytes.HasPrefix(text, joinKeyword) {
			// join room1
			roomName := string(text[len(joinKeyword)+1:])
			_, err := c.JoinRoom(nil, roomName)
			if err != nil {
				log.Printf("from server when trying to join to room[%s]: %v", roomName, err)
			}
			continue
		} else if bytes.HasPrefix(text, leaveKeyword) {
			roomName := string(text[len(leaveKeyword)+1:])
			if roomName == "all" {
				c.LeaveAll(nil)
			} else {
				c.Room(roomName).Leave(nil)
			}
			continue
		}

		if rooms := c.Rooms(); len(rooms) > 0 {
			for _, room := range rooms {
				ok := room.Emit("chat", text)
				if !ok {
					// this should never happen.
					log.Printf("tried to send to a left room[%s]", room.Name)
					break
				}
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
