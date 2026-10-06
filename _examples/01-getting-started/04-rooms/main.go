// Step 4 of 12: rooms inside the namespace.
//
// A room is a named group inside a namespace that a connection joins and
// leaves at will. The client calls NSConn.JoinRoom, which asks the server
// first: the server's OnRoomJoin handler can refuse by returning an error,
// and this one keeps the "staff" room for the names in the staff list. Once
// joined, OnRoomJoined fires on both sides; Room.Leave (or NSConn.LeaveAll
// for every room at once) fires OnRoomLeave and OnRoomLeft. When a connection
// closes, neffos leaves its rooms for it and sets Message.IsForced, so the
// server can tell a goodbye from a dropped line.
//
// Room.Emit sends with Message.Room filled. The server relays the message as
// it is, and because Message.Room is set, BroadcastOthers reaches only the
// connections in that room. NSConn.Rooms lists the rooms a connection has
// joined.
//
// Learn: join, leave and gate rooms, and send to the members of one room.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> /join general                # both terminals; each prints "you joined #general", alice also "* bob joined #general"
//	> hi room                      # bob's terminal; alice prints "#general bob: hi room"
//	> /join staff                  # bob's terminal; refused with "the staff room is for staff only"
//	> /rooms                       # lists the rooms you are in
//	> /leave                       # bob's terminal; alice prints "* bob left #general"
//
// Next: step 5 sends JSON bodies and binary frames (../05-encoding).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const (
	serverAddr = ":8080"
	clientURL  = "ws://localhost:8080/ws"
	namespace  = "chat"
)

// staff may join the "staff" room. Step 7 replaces names with real users.
var staff = map[string]bool{"alice": true}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "server":
		runServer(serverAddr)
	case "client":
		if len(os.Args) < 3 {
			usage()
		}
		runClient(clientURL, os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name>")
	os.Exit(2)
}

// notice builds a server announcement for the chat namespace.
func notice(text string) neffos.Message {
	return neffos.Message{Namespace: namespace, Event: "Notice", Body: []byte(text)}
}

var serverEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] entered %q", c.Conn.ID(), msg.Namespace)
			online := c.Conn.Server().GetTotalConnections()
			c.BroadcastOthers(notice(fmt.Sprintf("%s joined (%d online)", c.Conn.ID(), online)))
			return nil
		},
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] left %q", c.Conn.ID(), msg.Namespace)
			// Same as c.BroadcastOthers, written with the ID only.
			c.Conn.Server().Broadcast(neffos.Exclude(c.Conn.ID()), notice(c.Conn.ID()+" left"))
			return nil
		},
		neffos.OnRoomJoin: func(c *neffos.NSConn, msg neffos.Message) error {
			if msg.Room == "staff" && !staff[c.Conn.ID()] {
				return errors.New("the staff room is for staff only")
			}
			return nil
		},
		neffos.OnRoomJoined: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] joined #%s", c.Conn.ID(), msg.Room)
			n := notice(c.Conn.ID() + " joined #" + msg.Room)
			n.Room = msg.Room // only the members of the room get it
			c.BroadcastOthers(n)
			return nil
		},
		neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
			how := "left"
			if msg.IsForced {
				how = "dropped from" // the connection closed while in the room
			}
			log.Printf("[%s] %s #%s", c.Conn.ID(), how, msg.Room)
			n := notice(c.Conn.ID() + " " + how + " #" + msg.Room)
			n.Room = msg.Room
			c.BroadcastOthers(n)
			return nil
		},
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] says in %q: %s", c.Conn.ID(), msg.Room, msg.Body)
			msg.Body = []byte(c.Conn.ID() + ": " + string(msg.Body))
			c.BroadcastOthers(msg) // msg.Room is kept: a room message stays in its room
			return nil
		},
		"Private": func(c *neffos.NSConn, msg neffos.Message) error {
			to, text, _ := strings.Cut(string(msg.Body), " ")
			c.Conn.Server().Broadcast(nil, neffos.Message{
				To:        to, // one connection ID: only that connection receives it
				Namespace: namespace,
				Event:     "Private",
				Body:      []byte(c.Conn.ID() + ": " + text),
			})
			return nil
		},
	},
}

var clientEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf("you are in %q as %s\n", msg.Namespace, c.Conn.ID())
			return nil
		},
		neffos.OnRoomJoined: func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf("you joined #%s\n", msg.Room)
			return nil
		},
		neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf("you left #%s\n", msg.Room)
			return nil
		},
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			if msg.Room != "" {
				fmt.Printf("#%s ", msg.Room)
			}
			fmt.Printf("%s\n", msg.Body)
			return nil
		},
		"Private": show("(private) %s\n"),
		"Notice":  show("* %s\n"),
	},
}

// show returns a client handler that prints the message body with format.
func show(format string) neffos.MessageHandlerFunc {
	return func(c *neffos.NSConn, msg neffos.Message) error {
		fmt.Printf(format, msg.Body)
		return nil
	}
}

// newServer builds the websocket server. It is the seam the tests of step 11
// and the browser page of step 12 use, so it never listens by itself.
func newServer() *neffos.Server {
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	// The ID is the user name: the X-Username header for Go clients,
	// ?name= for browsers. Without either, fall back to a random ID.
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if name := r.Header.Get("X-Username"); name != "" {
			return name
		}
		if name := r.URL.Query().Get("name"); name != "" {
			return name
		}
		return neffos.DefaultIDGenerator(w, r)
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("upgrade failed: %v", err)
	}
	srv.OnConnect = func(c *neffos.Conn) error {
		log.Printf("[%s] connected", c.ID())
		return nil // an error here refuses the connection
	}
	srv.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected", c.ID())
	}

	return srv
}

func runServer(addr string) {
	srv := newServer()
	go operator(srv)

	mux := http.NewServeMux()
	mux.Handle("/ws", srv)

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// operator reads the server's terminal. Every line goes out as a notice.
func operator(srv *neffos.Server) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		srv.Broadcast(nil, notice("operator: "+line))
	}
}

func runClient(addr, name string) {
	ctx, cancel := deadline()
	defer cancel()

	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": {name}})
	client, err := neffos.Dial(ctx, dialer, addr, clientEvents)
	if err != nil {
		log.Fatal(err)
	}
	// Whatever ends the loop below (/quit, the end of input, or the connection
	// closing from either side), this runs once, after it.
	defer func() {
		client.Close() // does nothing if the connection is already closed
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, namespace)
	if err != nil {
		log.Fatal(err)
	}

	var room *neffos.Room // where plain lines go; nil means the whole namespace

	for line := range input(client) {
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "":
		case "/quit":
			return
		case "/msg": // /msg bob some text
			c.Emit("Private", []byte(arg))
		case "/join": // /join general
			ctx, cancel := deadline()
			r, err := c.JoinRoom(ctx, arg)
			cancel()
			if err != nil {
				fmt.Printf("cannot join #%s: %v\n", arg, err)
				continue
			}
			room = r
		case "/leave": // /leave leaves the current room, /leave all every room
			ctx, cancel := deadline()
			var err error
			if arg == "all" {
				err = c.LeaveAll(ctx)
			} else if room != nil {
				err = room.Leave(ctx)
			}
			cancel()
			if err != nil {
				fmt.Printf("cannot leave: %v\n", err)
			}
			room = nil
		case "/rooms":
			for _, r := range c.Rooms() {
				fmt.Printf("#%s\n", r.Name)
			}
		default:
			if room != nil {
				room.Emit("Chat", []byte(line))
				continue
			}
			c.Emit("Chat", []byte(line))
		}
	}
}

// deadline bounds a dial, a connect or a question so the client never waits forever.
func deadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

// input returns the lines typed in the terminal, trimmed. The channel closes
// at the end of input or as soon as the connection closes, whichever side
// closed it, so a loop over it always ends.
func input(client *neffos.Client) <-chan string {
	typed := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			typed <- strings.TrimSpace(scanner.Text())
		}
		close(typed)
	}()

	lines := make(chan string)
	go func() {
		defer close(lines)
		for {
			select {
			case <-client.NotifyClose:
				return
			case line, ok := <-typed:
				if !ok {
					return
				}
				lines <- line
			}
		}
	}()
	return lines
}
