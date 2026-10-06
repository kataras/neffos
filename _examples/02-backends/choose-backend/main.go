// One program, three websocket libraries: pick the backend with a flag.
//
// neffos does not speak the websocket protocol itself. A neffos.Upgrader
// turns an HTTP request into a neffos.Socket on the server, and a
// neffos.Dialer opens one from a client; the events code above them is the
// same whichever library does the work. Each backend package has a ready
// pair: gorilla.DefaultUpgrader and gorilla.DefaultDialer
// (github.com/gorilla/websocket), gobwas.DefaultUpgrader and
// gobwas.DefaultDialer (github.com/gobwas/ws), coder.DefaultUpgrader and
// coder.DefaultDialer (github.com/coder/websocket).
//
// The -backend flag chooses the pair for this run. Server and client choose
// independently, because they only share the websocket protocol: a gobwas
// client talks to a coder server without either noticing. The server echoes
// every line with the name of its backend, so you can see who answered.
//
// Learn: switch the websocket library under neffos without touching the events code.
//
// Run:
//
//	go run main.go -backend coder server          # terminal 1
//	go run main.go -backend gobwas client alice   # terminal 2
//
// Try:
//
//	> hello                        # alice's terminal prints "coder server: alice said hello"
//	go run main.go -backend gorilla client bob    # terminal 3; bob's lines are answered by the same coder server
//
// Next: ../custom-options builds the upgraders and dialers with options instead of the defaults.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/coder"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

// backend is an upgrader and a dialer from the same library.
type backend struct {
	upgrader neffos.Upgrader
	dialer   neffos.Dialer
}

var backends = map[string]backend{
	"gorilla": {gorilla.DefaultUpgrader, gorilla.DefaultDialer},
	"gobwas":  {gobwas.DefaultUpgrader, gobwas.DefaultDialer},
	"coder":   {coder.DefaultUpgrader, coder.DefaultDialer},
}

func main() {
	name := flag.String("backend", "gorilla", "websocket library: gorilla, gobwas or coder")
	flag.Parse()

	b, ok := backends[*name]
	if !ok || flag.NArg() < 1 {
		usage()
	}

	switch flag.Arg(0) {
	case "server":
		runServer(":8080", *name, b.upgrader)
	case "client":
		if flag.NArg() < 2 {
			usage()
		}
		runClient("ws://localhost:8080/ws", flag.Arg(1), b.dialer)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go [-backend gorilla|gobwas|coder] server | client <name>")
	os.Exit(2)
}

func runServer(addr, backendName string, upgrader neffos.Upgrader) {
	srv := neffos.New(upgrader, neffos.Namespaces{
		"chat": neffos.Events{
			"Echo": func(c *neffos.NSConn, msg neffos.Message) error {
				log.Printf("[%s] %s", c.Conn.ID(), msg.Body)
				return neffos.Reply(fmt.Appendf(nil, "%s server: %s said %s", backendName, c.Conn.ID(), msg.Body))
			},
		},
	})
	// ?name= works with every dialer, so the client sends its name that way.
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if name := r.URL.Query().Get("name"); name != "" {
			return name
		}
		return neffos.DefaultIDGenerator(w, r)
	}

	http.Handle("/ws", srv)
	log.Printf("%s server listening on %s, websocket endpoint /ws", backendName, addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(url, name string, dialer neffos.Dialer) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, dialer, url+"?name="+name, neffos.Namespaces{
		"chat": neffos.Events{
			"Echo": func(c *neffos.NSConn, msg neffos.Message) error {
				fmt.Println(string(msg.Body))
				return nil
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}

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

// input returns the lines typed in the terminal, trimmed. The channel closes
// at the end of input or as soon as the connection closes.
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
