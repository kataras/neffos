// Step 7 of 12: a token on the handshake and the user on the connection.
//
// A *neffos.Server is an http.Handler, so ordinary HTTP middleware runs before
// the upgrade. authenticate reads a bearer token from the Authorization
// header (Go clients) or from ?token= (browsers cannot set handshake headers)
// and answers 401 Unauthorized when it is missing or unknown; no websocket is
// opened at all. The demo tokens are the user name plus "-token", and the Go
// client sends that for the name it was started with.
//
// Server.OnConnect runs once the websocket is up and can still refuse it:
// returning an error closes the connection and the client's Dial returns
// that error's text. Here it rejects banned users, then stores the user on
// the connection with Conn.Set. Conn.Socket().Request() is the handshake
// request, so the token is read again from there, and later handlers read
// the user back with Conn.Get: the "staff" room now checks user.Staff
// instead of a list of names. OnConnect also refuses an unknown token, so the
// server stays closed when it is mounted without the middleware, as the
// tests of step 11 do.
//
// Learn: authenticate the upgrade request, refuse a connection from OnConnect and keep per-connection data with Conn.Set and Conn.Get.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2, staff
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	go run main.go client eve      # refused before the upgrade: "websocket: bad handshake" (401)
//	go run main.go client mallory  # refused by OnConnect: "mallory is banned"
//	> /join staff                  # bob is refused, alice gets in
//	curl -i localhost:8080/ws      # 401 Unauthorized
//
// Next: step 8 adds deadlines, a heartbeat and a message size limit (../08-timeouts-and-limits).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
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

// user is what the server knows about a connection once it is authenticated.
type user struct {
	Name   string
	Staff  bool // may join the "staff" room
	Banned bool
}

// users maps a bearer token to its user. A real app asks its identity provider.
var users = map[string]user{
	"alice-token":   {Name: "alice", Staff: true},
	"bob-token":     {Name: "bob"},
	"carol-token":   {Name: "carol"},
	"mallory-token": {Name: "mallory", Banned: true},
}

// lookupUser finds the user of a handshake request by its token.
func lookupUser(r *http.Request) (user, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		token = r.URL.Query().Get("token")
	}
	u, ok := users[token]
	return u, ok
}

// authenticate refuses a handshake without a known token before the upgrade.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := lookupUser(r); !ok {
			http.Error(w, "missing or unknown token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// chatMessage is the JSON body of the Chat and Private events.
type chatMessage struct {
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	Text string `json:"text"`
}

// Marshal makes chatMessage a neffos.MessageObjectMarshaler, so neffos.Marshal
// calls it instead of the default json.Marshal. It is the one place to change
// if the wire format ever changes.
func (m chatMessage) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// The two answers to "/who room" that are not a list: nobody is in that
// room, or the asker is not in it.
var (
	errEmptyRoom = errors.New("nobody is in that room")
	errNotInRoom = errors.New("not in that room")
)

// Both sides run this program, so one init registers the error on both.
func init() {
	neffos.RegisterKnownError(errEmptyRoom)
	neffos.RegisterKnownError(errNotInRoom)
}

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
			u, _ := c.Conn.Get("user").(user) // stored by OnConnect
			if msg.Room == "staff" && !u.Staff {
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
			var m chatMessage
			if err := msg.Unmarshal(&m); err != nil {
				return err
			}
			log.Printf("[%s] says in %q: %s", c.Conn.ID(), msg.Room, m.Text)
			m.From = c.Conn.ID() // the server decides who is speaking
			msg.Body = neffos.Marshal(m)
			c.BroadcastOthers(msg) // msg.Room is kept: a room message stays in its room
			return nil
		},
		"Private": func(c *neffos.NSConn, msg neffos.Message) error {
			var m chatMessage
			if err := msg.Unmarshal(&m); err != nil {
				return err
			}
			if _, online := c.Conn.Server().GetConnections()[m.To]; !online {
				return fmt.Errorf("%s is not online", m.To) // the sender gets it as Message.Err
			}
			m.From = c.Conn.ID()
			c.Conn.Server().Broadcast(nil, neffos.Message{
				To:        m.To, // one connection ID: only that connection receives it
				Namespace: namespace,
				Event:     "Private",
				Body:      neffos.Marshal(m),
			})
			return nil
		},
		"Wave": func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] waved, binary=%v, %d bytes", c.Conn.ID(), msg.SetBinary, len(msg.Body))
			c.BroadcastOthers(msg) // SetBinary is kept, so it goes out binary too
			return nil
		},
		"Who": func(c *neffos.NSConn, msg neffos.Message) error {
			room := string(msg.Body) // empty means the whole namespace
			var names []string
			for id, other := range c.Conn.Server().GetConnectionsByNamespace(namespace) {
				if room == "" || other.Room(room) != nil {
					names = append(names, id)
				}
			}
			switch {
			case len(names) == 0:
				return errEmptyRoom
			case room != "" && c.Room(room) == nil:
				return errNotInRoom
			}
			slices.Sort(names)
			return neffos.Reply(neffos.Marshal(names))
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
			var m chatMessage
			if err := msg.Unmarshal(&m); err != nil {
				return err
			}
			if msg.Room != "" {
				fmt.Printf("#%s ", msg.Room)
			}
			fmt.Printf("%s: %s\n", m.From, m.Text)
			return nil
		},
		"Private": func(c *neffos.NSConn, msg neffos.Message) error {
			if msg.Err != nil { // the server refused our own private message
				fmt.Printf("not sent: %v\n", msg.Err)
				return nil
			}
			var m chatMessage
			if err := msg.Unmarshal(&m); err != nil {
				return err
			}
			fmt.Printf("(private) %s: %s\n", m.From, m.Text)
			return nil
		},
		"Ping": func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Println("* the operator pinged you")
			return neffos.Reply([]byte("pong from " + c.Conn.ID()))
		},
		"Notice": show("* %s\n"),
		"Wave": func(c *neffos.NSConn, msg neffos.Message) error {
			frame := neffos.TextMessage
			if msg.SetBinary {
				frame = neffos.BinaryMessage
			}
			fmt.Printf("wave in a %s frame: % x\n", frame, msg.Body)
			return nil
		},
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
	// The ID is the user name the token belongs to. Without a known token,
	// fall back to a random ID; OnConnect refuses that connection anyway.
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if u, ok := lookupUser(r); ok {
			return u.Name
		}
		return neffos.DefaultIDGenerator(w, r)
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("upgrade failed: %v", err)
	}
	srv.OnConnect = func(c *neffos.Conn) error {
		u, ok := lookupUser(c.Socket().Request())
		switch {
		case !ok:
			return errors.New("missing or unknown token")
		case u.Banned:
			log.Printf("[%s] refused: banned", c.ID())
			return fmt.Errorf("%s is banned", u.Name)
		}

		c.Set("user", u)
		log.Printf("[%s] connected", c.ID())
		return nil
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
	mux.Handle("/ws", authenticate(srv))

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// operator reads the server's terminal. "ping <name>" asks that client for an
// answer; every other line goes out as a notice.
func operator(srv *neffos.Server) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if name, ok := strings.CutPrefix(line, "ping "); ok {
			ctx, cancel := deadline()
			reply, err := srv.Ask(ctx, neffos.Message{To: name, Namespace: namespace, Event: "Ping"})
			cancel()
			if err != nil {
				log.Printf("ping %s: %v", name, err)
				continue
			}
			log.Printf("ping %s: %s", name, reply.Body)
			continue
		}

		srv.Broadcast(nil, notice("operator: "+line))
	}
}

func runClient(addr, name string) {
	ctx, cancel := deadline()
	defer cancel()

	token := name + "-token" // the demo convention; a real client gets it from a login
	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"Authorization": {"Bearer " + token}})
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
			to, text, _ := strings.Cut(arg, " ")
			c.Emit("Private", neffos.Marshal(chatMessage{To: to, Text: text}))
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
		case "/wave":
			c.EmitBinary("Wave", []byte{0x00, 0x7f, 0xff, 0x7f})
		case "/who": // /who for everyone online, /who general for one room
			ctx, cancel := deadline()
			reply, err := c.Ask(ctx, "Who", []byte(arg))
			cancel()
			switch {
			case errors.Is(err, errNotInRoom):
				fmt.Printf("you are not in #%s\n", arg)
			case errors.Is(err, errEmptyRoom):
				fmt.Printf("nobody is in #%s\n", arg)
			case err != nil:
				fmt.Printf("who: %v\n", err)
			default:
				var names []string
				reply.Unmarshal(&names)
				fmt.Printf("online: %s\n", strings.Join(names, ", "))
			}
		default:
			body := neffos.Marshal(chatMessage{Text: line})
			if room != nil {
				room.Emit("Chat", body)
				continue
			}
			c.Emit("Chat", body)
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
