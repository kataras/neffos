// Step 3 of 12: relay chat to everyone else, notices and private messages.
//
// The server and the client now have their own events: serverEvents decides
// who gets what, clientEvents prints it. A chat line is relayed with
// NSConn.BroadcastOthers, which sends to every connection except the sender
// (NSConn.Broadcast would include the sender). Joins and leaves become
// "Notice" events for the others; the join notice counts the people online
// with Server.GetTotalConnections. The leave notice goes through
// Server.Broadcast with neffos.Exclude, the form to use when all you hold is
// a connection ID. A broadcast does not wait for the receivers: every
// connection gets its own copy and writes it at its own pace, and two
// broadcasts in a row arrive in the order they were made.
//
// Server.Broadcast(nil, msg) reaches everyone: the operator types a line in
// the server's terminal and every client gets it as a notice. Setting
// Message.To narrows a broadcast to one connection ID, which is all a private
// message needs: "/msg bob hi" reaches bob alone.
//
// Learn: send to everyone, everyone but the sender, or one connection by ID.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> hello                        # bob's terminal; alice prints "bob: hello", bob prints nothing
//	> /msg alice psst              # bob's terminal; only alice prints "(private) bob: psst"
//	maintenance at five            # the server's terminal; both print "* operator: maintenance at five"
//	> /quit                        # bob's terminal; alice prints "* bob left"
//
// Next: step 4 adds rooms inside the namespace (../04-rooms).
package main

import (
	"bufio"
	"context"
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
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] says: %s", c.Conn.ID(), msg.Body)
			msg.Body = []byte(c.Conn.ID() + ": " + string(msg.Body))
			c.BroadcastOthers(msg)
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
		"Chat":    show("%s\n"),
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

	for line := range input(client) {
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "":
		case "/quit":
			return
		case "/msg": // /msg bob some text
			c.Emit("Private", []byte(arg))
		default:
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
