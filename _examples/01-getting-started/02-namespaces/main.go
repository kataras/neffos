// Step 2 of 12: a chat namespace, user names and lifecycle logs.
//
// A neffos connection talks inside namespaces. neffos.Namespaces maps a
// namespace name to its Events, and a client has to Connect to a namespace
// before it can emit there. This step moves the app into the "chat"
// namespace and logs every stage of a connection's life: Server.OnConnect and
// Server.OnDisconnect for the websocket itself, OnNamespaceConnect (which can
// refuse by returning an error), then OnNamespaceConnected and
// OnNamespaceDisconnect for the namespace. Server.OnUpgradeError reports
// requests that never became a websocket.
//
// The connection ID is now the user name. Server.IDGenerator reads it from
// the X-Username header, which the Go client sets through gorilla.Dialer, or
// from the ?name= query parameter, because browsers cannot set handshake
// headers. The server answers each chat line with the sender's ID in front,
// so you can see the name arrive on the other side.
//
// Learn: serve a namespace, name connections with IDGenerator and follow a connection's lifecycle.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//
// Try:
//
//	> hello                        # alice's terminal prints "alice: hello"; the server logs "[alice] says: hello"
//	> /quit                        # the server logs that alice left "chat", then "[alice] disconnected"
//	curl -i localhost:8080/ws      # 400 Bad Request; the server logs "upgrade failed: ..."
//
// Next: step 3 relays messages to everyone else and adds private messages (../03-broadcast).
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

// events is shared by the server and the client in this step; step 3 splits it.
var events = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnect: func(c *neffos.NSConn, msg neffos.Message) error {
			if !c.Conn.IsClient() {
				log.Printf("[%s] asks to enter %q", c.Conn.ID(), msg.Namespace)
			}
			return nil // an error here refuses the namespace
		},
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			if c.Conn.IsClient() {
				fmt.Printf("you are in %q as %s\n", msg.Namespace, c.Conn.ID())
				return nil
			}

			log.Printf("[%s] entered %q", c.Conn.ID(), msg.Namespace)
			return nil
		},
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			if !c.Conn.IsClient() {
				log.Printf("[%s] left %q", c.Conn.ID(), msg.Namespace)
			}
			return nil
		},
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			if c.Conn.IsClient() {
				fmt.Printf("%s\n", msg.Body)
				return nil
			}

			log.Printf("[%s] says: %s", c.Conn.ID(), msg.Body)
			return neffos.Reply([]byte(c.Conn.ID() + ": " + string(msg.Body)))
		},
	},
}

// newServer builds the websocket server. It is the seam the tests of step 11
// and the browser page of step 12 use, so it never listens by itself.
func newServer() *neffos.Server {
	srv := neffos.New(gorilla.DefaultUpgrader, events)

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
	mux := http.NewServeMux()
	mux.Handle("/ws", newServer())

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(addr, name string) {
	ctx, cancel := deadline()
	defer cancel()

	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": {name}})
	client, err := neffos.Dial(ctx, dialer, addr, events)
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
		switch line {
		case "":
		case "/quit":
			return
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
