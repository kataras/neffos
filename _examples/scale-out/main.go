package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/stackexchange/nats"
	"github.com/kataras/neffos/stackexchange/redis"
)

/*
	# Two LOC(lines of code) to scale-out neffos servers.
	#
	# Download Redis or Nats.
	# Start Redis or Nats server with default configuration.
	#
	# Start two terminal sessions on this directory and execute:
	#
	$ go run main.go server :8080
	$ go run main.go server :9090
	#
	# Or for Nats:
	#
	$ go run main.go server :8080 nats
	$ go run main.go server :9090 nats
	#
	# Open some browser tabs at:
	# http://localhost:8080 and
	# http://localhost:9090
	#
	# Start to send and receive room or private messages
	# across two different neffos servers.
*/

var addr = "localhost:8080"

const (
	endpoint  = "/echo"
	namespace = "default"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	if len(args) >= 2 {
		addr = args[1]
	}

	scaleOutBackend := "redis"
	if len(args) == 3 {
		scaleOutBackend = args[2]
	}

	switch side {
	case "server":
		var stackExchange neffos.StackExchange
		switch scaleOutBackend {
		case "redis":
			redisExc, err := redis.NewStackExchange(redis.Config{}, "MyChatApp")
			if err != nil {
				panic(err)
			}

			stackExchange = redisExc
		case "nats":
			natsExc, err := nats.NewStackExchange("0.0.0.0:4222")
			//                                     ^^^^^
			// more servers can be used with comma separated url input argument.
			if err != nil {
				panic(err)
			}

			stackExchange = natsExc
		default:
			log.Fatalf("unexpected last argument, expected 'redis' or 'nats' but got '%s'", scaleOutBackend)
		}

		log.Printf("Using %s to scale out", scaleOutBackend)
		startServer(stackExchange)
	case "client":
		startClient()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

// userMessage implements the `MessageBodyUnmarshaler` and `MessageBodyMarshaler`.
type userMessage struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text"`
}

// Defaults to `DefaultUnmarshaler & DefaultMarshaler` that are calling the json.Unmarshal & json.Marshal respectfully
// if the instance's Marshal and Unmarshal methods are missing.
func (u *userMessage) Marshal() ([]byte, error) {
	return json.Marshal(u)
}

func (u *userMessage) Unmarshal(b []byte) error {
	return json.Unmarshal(b, u)
}

func askClient(server *neffos.Server, toID string) string {
	response, err := server.Ask(nil, neffos.Message{
		To:        toID,
		Namespace: namespace,
		Event:     "onAsk",
		Body:      []byte("how are you?"),
	})
	if err != nil {
		panic(err)
	}

	return string(response.Body)
}

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
			log.Printf("%s", text)

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
			log.Printf("%s", text)

			if !c.Conn.IsClient() && !msg.IsForced {
				go func(c *neffos.Conn) {
					reply := askClient(c.Server(), c.ID())
					log.Printf("Asked client if it's OK but replied: %s", reply)
				}(c.Conn)
			}

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
			var userMsg userMessage
			err := msg.Unmarshal(&userMsg)
			if err != nil {
				log.Fatal(err)
			}

			if !c.Conn.IsClient() {
				if userMsg.To != "" {
					// msg.To is not exposed, so you have to add it manually
					// based on your user messages, like we do here.
					// Also Server.Broadcast(..., Message{To:...})
					// is possible when custom user message not required.
					msg.To = userMsg.To
				}

				c.Conn.Server().Broadcast(c, msg)
			} else {
				if userMsg.To != "" {
					userMsg.From = "private message: " + userMsg.From
				}

			}

			fmt.Printf("%s >> [%s] says: %s\n", msg.Room, userMsg.From, userMsg.Text)

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
		// client-side only event to reply to server requests.
		"onAsk": func(c *neffos.NSConn, msg neffos.Message) error {
			// log.Println("On Ask: " + string(msg.Body))

			if !c.Conn.IsClient() {
				return nil
			}
			return neffos.Reply([]byte("dude, let me alone please"))
		},
	},
}

func startServer(stackExchange neffos.StackExchange) {
	server := neffos.New(gobwas.DefaultUpgrader, serverAndClientEvents)

	server.UseStackExchange(stackExchange)
	// ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
	// The server.StackExchange field is also exported
	// so users can directly use or/and test their registered
	// implementations all together.
	// This is possible because a wrapper is in-place
	// when you register more than one stack exchanges
	// on the same neffos server instance.

	server.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if userID := r.Header.Get("X-Username"); userID != "" {
			return userID
		}

		return neffos.DefaultIDGenerator(w, r)
	}

	server.OnUpgradeError = func(err error) {
		log.Printf("ERROR: %v", err)
	}

	server.OnConnect = func(c *neffos.Conn) error {
		if c.WasReconnected() {
			log.Printf("[%s] connection is a result of a client-side re-connection, with tries: %d", c.ID(), c.ReconnectTries)
		}

		log.Printf("[%s] connected to the server.", c)
		// if returns non-nil error then it refuses the client to connect to the server.
		return nil
	}

	server.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected from the server.", c)
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", addr)
	http.Handle("/", http.FileServer(http.Dir("./browser")))
	http.Handle(endpoint, server)
	log.Fatal(http.ListenAndServe(addr, nil))
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
		addr+endpoint,
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

		text := scanner.Text()

		if text == "exit" {
			break
		}

		if text == "leave" {
			room.Leave(nil)
			goto askRoom
		}

		// username is the connection's ID ==
		// room.String() returns -> NSConn.String() returns -> Conn.String() returns -> Conn.ID()
		// which generated by server-side via `Server#IDGenerator`.
		userMsg := userMessage{From: username, Text: text}
		room.Emit("chat", neffos.Marshal(userMsg))

		fmt.Fprint(os.Stdout, ">> ")
	}
}
