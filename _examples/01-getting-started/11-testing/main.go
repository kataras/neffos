// Step 11 of 12: testing the server with real Go clients.
//
// This main.go is step 10's, unchanged; the step is main_test.go. newServer
// returns the *neffos.Server without listening, so a test mounts it on
// httptest.NewServer, which picks a free port, and dials it with ordinary
// neffos Go clients. Every test starts its own server, so no test sees
// another's connections, rooms or broadcasts.
//
// A test client records every event it receives into a channel. Assertions
// wait on that channel with select and a timeout, so a message that never
// comes fails the test after two seconds instead of hanging it, and a
// message that must not come is checked over a short quiet period. The
// tests cover the broadcast, the rooms, both directions of Ask with their
// known errors, the close codes of steps 8 and 9, and the refused
// connections.
//
// Learn: test a neffos server end to end with httptest and two Go clients.
//
// Run:
//
//	go test -v -count=1 .          # runs main_test.go
//	go run main.go server          # the app itself, the same as step 10
//
// Try:
//
//	go test -run Rooms -v .        # just the rooms test
//
// Next: step 12 serves the same chat to the browser (../12-browser-client).
package main

import (
	"bufio"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"os/signal"
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

	// The limits of step 8. The read deadline is three heartbeats long, so a
	// connection that answers pings never reaches it.
	readTimeout    = 30 * time.Second
	writeTimeout   = 10 * time.Second
	pingInterval   = 10 * time.Second
	maxMessageSize = 4 << 10 // bytes in one incoming message

	// closeKicked is the close code of a kicked user. Codes 4000 to 4999 are
	// free for applications to use.
	closeKicked = 4000
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
	To   string `json:"to,omitzero"`
	Text string `json:"text"`
}

// Marshal makes chatMessage a neffos.MessageObjectMarshaler, so neffos.Marshal
// calls it instead of the default encoder. It is the one place to change if
// the wire format ever changes.
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

// lobby handles the chat namespace on the server; its methods are the events.
// The Conn field makes it a per-connection value: neffos creates a new lobby
// for every connection and fills Conn. Topic is set once, on the value given
// to NewStruct, and copied into every new lobby.
type lobby struct {
	Conn  *neffos.NSConn
	Topic string
}

func (l *lobby) id() string { return l.Conn.Conn.ID() }

func (l *lobby) OnNamespaceConnected(msg neffos.Message) error {
	log.Printf("[%s] entered %q", l.id(), msg.Namespace)
	online := l.Conn.Conn.Server().GetTotalConnections()
	l.Conn.BroadcastOthers(notice(fmt.Sprintf("%s joined (%d online)", l.id(), online)))
	l.Conn.Emit("Notice", []byte("topic: "+l.Topic))
	return nil
}

func (l *lobby) OnNamespaceDisconnect(msg neffos.Message) error {
	log.Printf("[%s] left %q", l.id(), msg.Namespace)
	// Same as l.Conn.BroadcastOthers, written with the ID only.
	l.Conn.Conn.Server().Broadcast(neffos.Exclude(l.id()), notice(l.id()+" left"))
	return nil
}

func (l *lobby) OnRoomJoin(msg neffos.Message) error {
	u, _ := l.Conn.Conn.Value[user]("user") // stored by OnConnect
	if msg.Room == "staff" && !u.Staff {
		return errors.New("the staff room is for staff only")
	}
	return nil
}

func (l *lobby) OnRoomJoined(msg neffos.Message) error {
	log.Printf("[%s] joined #%s", l.id(), msg.Room)
	n := notice(l.id() + " joined #" + msg.Room)
	n.Room = msg.Room // only the members of the room get it
	l.Conn.BroadcastOthers(n)
	return nil
}

func (l *lobby) OnRoomLeft(msg neffos.Message) error {
	how := "left"
	if msg.IsForced {
		how = "dropped from" // the connection closed while in the room
	}
	log.Printf("[%s] %s #%s", l.id(), how, msg.Room)
	n := notice(l.id() + " " + how + " #" + msg.Room)
	n.Room = msg.Room
	l.Conn.BroadcastOthers(n)
	return nil
}

func (l *lobby) OnChat(msg neffos.Message) error {
	m, err := msg.As[chatMessage]()
	if err != nil {
		return err
	}
	log.Printf("[%s] says in %q: %s", l.id(), msg.Room, m.Text)
	m.From = l.id() // the server decides who is speaking
	msg.Body, err = neffos.Marshal(m)
	if err != nil {
		return err
	}
	l.Conn.BroadcastOthers(msg) // msg.Room is kept: a room message stays in its room
	return nil
}

func (l *lobby) OnPrivate(msg neffos.Message) error {
	m, err := msg.As[chatMessage]()
	if err != nil {
		return err
	}
	if _, online := l.Conn.Conn.Server().GetConnections()[m.To]; !online {
		return fmt.Errorf("%s is not online", m.To) // the sender gets it as Message.Err
	}
	m.From = l.id()
	body, err := neffos.Marshal(m)
	if err != nil {
		return err
	}
	l.Conn.Conn.Server().Broadcast(nil, neffos.Message{
		To:        m.To, // one connection ID: only that connection receives it
		Namespace: namespace,
		Event:     "Private",
		Body:      body,
	})
	return nil
}

func (l *lobby) OnWave(msg neffos.Message) error {
	log.Printf("[%s] waved, binary=%v, %d bytes", l.id(), msg.SetBinary, len(msg.Body))
	l.Conn.BroadcastOthers(msg) // SetBinary is kept, so it goes out binary too
	return nil
}

// who reads only the server, not the lobby value, so it stays a plain handler.
func who(c *neffos.NSConn, msg neffos.Message) error {
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
	return neffos.ReplyObject(names)
}

// controller turns lobby's methods into the chat namespace's events. The
// matcher registers OnChat as "Chat", the name the clients already emit;
// OnNamespaceConnected and the other lifecycle methods keep their meaning.
// The other setters carry the limits of step 8: the read and write
// deadlines, the heartbeat and the size of one incoming message.
var controller = neffos.NewStruct(&lobby{Topic: "be kind, no spoilers"}).
	SetNamespace(namespace).
	SetEventMatcher(neffos.EventTrimPrefixMatcher("On")).
	SetTimeouts(readTimeout, writeTimeout).
	SetPingInterval(pingInterval).
	SetMaxMessageSize(maxMessageSize)

// serverEvents merges the controller with the plain "Who" handler.
var serverEvents = neffos.JoinConnHandlers(controller, neffos.Namespaces{
	namespace: neffos.Events{"Who": who},
})

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
			m, err := msg.As[chatMessage]()
			if err != nil {
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
			m, err := msg.As[chatMessage]()
			if err != nil {
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
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents) // the limits come with the controller
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
		// Conn.Err says what closed the connection.
		switch err := c.Err(); {
		case errors.Is(err, neffos.ErrMessageTooBig):
			log.Printf("[%s] disconnected: sent a message over %d bytes", c.ID(), maxMessageSize)
		case neffos.IsTimeoutError(err):
			log.Printf("[%s] disconnected: timed out", c.ID())
		default:
			log.Printf("[%s] disconnected, close status %d", c.ID(), neffos.CloseStatus(err))
		}
	}

	return srv
}

func runServer(addr string) {
	srv := newServer()
	go operator(srv)

	mux := http.NewServeMux()
	mux.Handle("/ws", authenticate(srv))
	httpServer := &http.Server{Addr: addr, Handler: mux}

	// ctx is done on Ctrl+C. stop restores the default, so a second Ctrl+C
	// ends the program at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	events := slices.Sorted(maps.Keys(controller.Events()))
	log.Printf("lobby events: %s", strings.Join(events, ", "))
	go func() {
		log.Printf("listening on %s, websocket endpoint /ws", addr)
		if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Print("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("websocket shutdown: %v", err)
	}
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	log.Print("bye")
}

// operator reads the server's terminal. "ping <name>" asks that client for an
// answer, "kick <name>" closes its connection; every other line goes out as a
// notice.
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
				log.Printf("ping %s: %s", name, describe(err))
				continue
			}
			log.Printf("ping %s: %s", name, reply.Body)
			continue
		}

		if name, ok := strings.CutPrefix(line, "kick "); ok {
			c, online := srv.GetConnections()[name]
			if !online {
				log.Printf("kick %s: not online", name)
				continue
			}
			c.Terminate(closeKicked, "kicked by operator")
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
	client, err := neffos.Dial(ctx, dialer, addr, neffos.WithTimeout{
		WriteTimeout: writeTimeout,
		PingInterval: pingInterval, // the client checks on the server too
		Namespaces:   clientEvents,
	})
	if err != nil {
		log.Fatal(err)
	}
	// Whatever ends the loop below (/quit, the end of input, or the connection
	// closing from either side), this runs once, after it.
	defer func() {
		client.Close() // does nothing if the connection is already closed
		// Conn.Err holds the close frame the server sent, if it sent one.
		err := client.Conn().Err()
		switch code := neffos.CloseStatus(err); code {
		case neffos.CloseNormalClosure:
			fmt.Println("connection closed")
		case neffos.CloseMessageTooBig:
			fmt.Println("connection closed: the server refused a message over its size limit")
		case -1: // no close frame: the network or the heartbeat ended it
			fmt.Printf("connection lost: %s\n", describe(err))
		default:
			ce, _ := errors.AsType[neffos.CloseError](err)
			fmt.Printf("connection closed: %d %s\n", code, ce.Reason)
		}
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
			if err := c.SendObject("Private", chatMessage{To: to, Text: text}); err != nil {
				fmt.Printf("not sent: %s\n", describe(err))
			}
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
		case "/spam": // one chat line twice the size the server accepts
			text := strings.Repeat("spam ", 2*maxMessageSize/5)
			if err := c.SendObject("Chat", chatMessage{Text: text}); err != nil {
				fmt.Printf("not sent: %s\n", describe(err))
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
				fmt.Printf("who: %s\n", describe(err))
			default:
				names, _ := reply.As[[]string]()
				fmt.Printf("online: %s\n", strings.Join(names, ", "))
			}
		default:
			send := c.SendObject
			if room != nil {
				send = room.SendObject
			}
			if err := send("Chat", chatMessage{Text: line}); err != nil {
				fmt.Printf("not sent: %s\n", describe(err))
			}
		}
	}
}

// describe says in words why a Send or an Ask failed. IsDisconnectError is
// true for timeouts too, so the timeout case comes first.
func describe(err error) string {
	switch {
	case neffos.IsTimeoutError(err):
		return "timed out"
	case neffos.IsDisconnectError(err):
		return "the connection is closed"
	default:
		return err.Error()
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
