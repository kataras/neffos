package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
)

const (
	endpoint  = "localhost:8080"
	namespace = "default"
)

var serverAndClientEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] connected to namespace [%s].", c, msg.Namespace)
			return nil
		},
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] disconnected from namespace [%s].", c, msg.Namespace)
			return nil
		},

		neffos.OnRoomJoined: func(c *neffos.NSConn, msg neffos.Message) error {
			text := fmt.Sprintf("[%s] joined to room [%s].", c, msg.Room)
			log.Printf("\n%s", text)

			// notify others.
			if !c.Conn.IsClient() {
				c.Conn.Server().Broadcast(c, neffos.Message{
					Namespace: msg.Namespace,
					Room:      msg.Room,
					Event:     "notify",
					Body:      []byte(text),
				})
			}

			return nil
		},
		neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
			text := fmt.Sprintf("[%s] left from room [%s].", c, msg.Room)
			log.Printf("\n%s", text)

			// notify others.
			if !c.Conn.IsClient() {
				c.Conn.Server().Broadcast(c, neffos.Message{
					Namespace: msg.Namespace,
					Room:      msg.Room,
					Event:     "notify",
					Body:      []byte(text),
				})
			}

			return nil
		},

		"chat": func(c *neffos.NSConn, msg neffos.Message) error {
			if !c.Conn.IsClient() {
				c.Conn.Server().Broadcast(c, msg)
			} else {
				// make an OriginalFrom field?
				// fmt.Printf("[%s] says: %s\n", c, string(msg.Body))
			}
			return nil
		},
		// client-side only event to catch any server messages comes from the custom "notify" event.
		"notify": func(c *neffos.NSConn, msg neffos.Message) error {
			if !c.Conn.IsClient() {
				return nil
			}

			fmt.Println(string(msg.Body))
			return nil
		},
	},
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	switch side {
	case "server":
		startServer()
	case "client":
		startClient()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func startServer() {
	server := neffos.New(gobwas.DefaultUpgrader, serverAndClientEvents)
	server.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if userID := r.Header.Get("X-Username"); userID != "" {
			return userID
		}

		return neffos.DefaultIDGenerator(w, r)
	}

	server.OnConnect = func(c *neffos.Conn) error {
		log.Printf("[%s] connected to the server.", c)

		// if returns non-nil error then it refuses the client to connect to the server.
		return nil
	}
	server.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected from the server.", c)
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	log.Fatal(http.ListenAndServe(endpoint, server))
}

func startClient() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Please specify a username: ")
	if !scanner.Scan() {
		return
	}
	username := scanner.Text()

	// init the websocket connection by dialing the server.
	client, err := neffos.Dial(
		// Optional context cancelation and deadline for dialing.
		nil,
		// The underline dialer, can be also a gobwas.Dialer/DefautlDialer or a gorilla.Dialer/DefaultDialer.
		// Here we wrap a custom gobwas dialer in order to send the username among, on the handshake state,
		// see `startServer().server.IDGenerator`.
		gobwas.Dialer(gobwas.Options{Header: gobwas.Header{"X-Username": []string{username}}}),
		// The endpoint, i.e ws://localhost:8080/path.
		endpoint,
		// The namespaces and events, can be optionally shared with the server's.
		serverAndClientEvents)

	if err != nil {
		log.Fatal(err)
	}

	defer client.Close()

	go func() {
		<-client.NotifyClose
		os.Exit(0)
	}()

	// connect to the "default" namespace.
	c, err := client.Connect(nil, namespace)
	if err != nil {
		log.Fatal(err)
	}
askRoom:
	fmt.Print("Please specify a room to join, i.e room1: ")
	if !scanner.Scan() {
		log.Fatal(scanner.Err())
	}
	roomToJoin := scanner.Text()

	room, err := c.JoinRoom(nil, roomToJoin)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprint(os.Stdout, ">> ")

	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			break
		}

		text := scanner.Bytes()
		if bytes.Equal(text, []byte("exit")) {
			break
		}

		if bytes.Equal(text, []byte("leave")) {
			room.Leave(nil)
			goto askRoom
		}

		room.Emit("chat", text)

		fmt.Fprint(os.Stdout, ">> ")
	}
}
