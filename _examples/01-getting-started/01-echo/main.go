// Step 1 of 12: an echo server and its Go client.
//
// This is the first step of the Lobby tutorial, which grows one chat app from
// a single echo event to a tested server with a browser client. Every step is
// a complete program that adds one idea to the previous one, so you can diff
// two neighbours to see exactly what changed.
//
// neffos.New takes an Upgrader (gorilla.DefaultUpgrader here) and the events
// to handle, and returns a *neffos.Server, which is a plain http.Handler.
// neffos.Events maps an event name to a func(*neffos.NSConn, neffos.Message)
// error. On the other side neffos.Dial opens the connection, Client.Connect
// enters a namespace (the empty one in this step) and NSConn.Emit sends an
// event. A server handler that returns neffos.Reply sends a body straight
// back to the sender, on the same event. Both sides share one Events value in
// this step and tell themselves apart with Conn.IsClient.
//
// Learn: serve a neffos endpoint, dial it from Go and answer an event.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//
// Try:
//
//	> hello                        # alice's terminal prints "echo: hello"; the server logs it
//	> /quit                        # the client closes and prints "connection closed"
//
// Next: step 2 adds a chat namespace, user names and lifecycle logs (../02-namespaces).
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

// events is shared by the server and the client in this step. The server
// echoes the body back; the client prints what came back.
var events = neffos.Events{
	"Echo": func(c *neffos.NSConn, msg neffos.Message) error {
		if c.Conn.IsClient() {
			fmt.Printf("echo: %s\n", msg.Body)
			return nil
		}

		log.Printf("[%s] echo %q", c.Conn.ID(), msg.Body)
		return neffos.Reply(msg.Body)
	},
}

// newServer builds the websocket server. It is the seam the tests of step 11
// and the browser page of step 12 use, so it never listens by itself.
func newServer() *neffos.Server {
	return neffos.New(gorilla.DefaultUpgrader, events)
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

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr, events)
	if err != nil {
		log.Fatal(err)
	}
	// Whatever ends the loop below (/quit, the end of input, or the connection
	// closing from either side), this runs once, after it.
	defer func() {
		client.Close() // does nothing if the connection is already closed
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, "")
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("hi %s, type a line and press Enter, /quit to leave\n", name)
	for line := range input(client) {
		switch line {
		case "":
		case "/quit":
			return
		default:
			c.Emit("Echo", []byte(line))
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
