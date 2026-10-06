// Looking at live connections and acting on them from outside an event.
//
// The server always knows who is connected. Server.GetTotalConnections
// counts them, Server.GetConnections maps every connection ID to its
// *neffos.Conn, and Server.GetConnectionsByNamespace maps the IDs of the
// connections inside one namespace to their *neffos.NSConn. The operator
// console in the server's terminal uses them: "list" prints who is where.
//
// Server.Do runs a function once for every connection, outside any event
// callback. "news <text>" uses it to send a headline only to the
// connections that entered the "news" namespace, which Conn.Namespace
// tells. "drop <name>" calls Conn.DisconnectAll, which takes a connection
// out of every namespace and leaves the websocket open. Do runs on the
// server's dispatch goroutine, so a slow call like DisconnectAll, which
// waits for the client's answer, goes outside it.
//
// Three smaller things round it off. OnConnect refuses "mallory", and with
// Server.FireDisconnectAlways set, OnDisconnect still runs for her, so
// cleanup code sees every connection. OnUpgradeError logs requests that never
// became a websocket. Conn.ReconnectTries is the number a reconnecting client
// sends in the X-Websocket-Reconnect header, as neffos.js does; the Go client
// here takes it as an optional third argument to show what the server sees.
//
// Learn: inspect live connections and act on them with Server.Do, Conn.DisconnectAll and the server's hooks.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob 3    # terminal 3, as if redialing for the third time
//
// Try:
//
//	> /news                        # alice's terminal; alice enters the "news" namespace too
//	list                           # the server's terminal; logs "2 connections: alice (chat, news), bob (chat, reconnect 3)"
//	news rain at noon              # alice prints "headline: rain at noon"; the server logs "headline sent to 1 of 2"
//	drop alice                     # alice prints "left chat" and "left news"; list now shows "alice ()"
//	go run main.go client mallory  # refused: "mallory is not welcome"; the server still logs "[mallory] disconnected"
//	curl -i localhost:8080/ws      # 400 Bad Request; the server logs "upgrade failed: ..."
//
// Next: ../../06-server-push/http-to-websocket pushes to users from an HTTP handler.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

var namespaces = []string{"chat", "news"}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "server":
		runServer(":8080")
	case "client":
		if len(os.Args) < 3 {
			usage()
		}
		tries := ""
		if len(os.Args) > 3 {
			tries = os.Args[3]
		}
		runClient("ws://localhost:8080/ws", os.Args[2], tries)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name> [reconnect-tries]")
	os.Exit(2)
}

func newServer() *neffos.Server {
	srv := neffos.New(gorilla.DefaultUpgrader, neffos.Namespaces{
		"chat": neffos.Events{
			"Say": func(c *neffos.NSConn, msg neffos.Message) error {
				msg.Body = fmt.Appendf(nil, "%s: %s", c.Conn.ID(), msg.Body)
				c.BroadcastOthers(msg)
				return nil
			},
		},
		"news": neffos.Events{},
	})
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.Header.Get("X-Username")
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("upgrade failed: %v", err)
	}
	srv.OnConnect = func(c *neffos.Conn) error {
		if c.ID() == "mallory" {
			return fmt.Errorf("%s is not welcome", c.ID())
		}
		log.Printf("[%s] connected, reconnect tries: %d", c.ID(), c.ReconnectTries)
		return nil
	}
	// OnDisconnect runs even for connections that OnConnect refused.
	srv.FireDisconnectAlways = true
	srv.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected", c.ID())
	}
	return srv
}

func runServer(addr string) {
	srv := newServer()
	go operator(srv)

	http.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// operator reads the server's terminal: "list", "news <text>", "drop <name>".
func operator(srv *neffos.Server) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		cmd, arg, _ := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		switch cmd {
		case "list":
			list(srv)
		case "news":
			sent := 0
			srv.Do(func(c *neffos.Conn) {
				if ns := c.Namespace("news"); ns != nil && ns.Emit("Headline", []byte(arg)) {
					sent++ // Do with async=false returns after the last call, so no lock is needed
				}
			}, false)
			log.Printf("headline sent to %d of %d", sent, srv.GetTotalConnections())
		case "drop":
			c, ok := srv.GetConnections()[arg]
			if !ok {
				log.Printf("drop %s: not connected", arg)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			if err := c.DisconnectAll(ctx); err != nil {
				log.Printf("drop %s: %v", arg, err)
			}
			cancel()
		}
	}
}

// list logs every connection with the namespaces it is in.
func list(srv *neffos.Server) {
	in := map[string][]string{} // connection ID to namespaces
	for _, ns := range namespaces {
		for id := range srv.GetConnectionsByNamespace(ns) {
			in[id] = append(in[id], ns)
		}
	}

	conns := srv.GetConnections()
	var lines []string
	for _, id := range slices.Sorted(maps.Keys(conns)) {
		line := fmt.Sprintf("%s (%s", id, strings.Join(in[id], ", "))
		if c := conns[id]; c.WasReconnected() {
			line += fmt.Sprintf(", reconnect %d", c.ReconnectTries)
		}
		lines = append(lines, line+")")
	}
	log.Printf("%d connections: %s", srv.GetTotalConnections(), strings.Join(lines, ", "))
}

func runClient(addr, name, reconnectTries string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	header := http.Header{"X-Username": {name}}
	if reconnectTries != "" {
		header.Set("X-Websocket-Reconnect", reconnectTries)
	}

	print := func(format string) neffos.MessageHandlerFunc {
		return func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf(format, msg.Body)
			return nil
		}
	}
	left := func(c *neffos.NSConn, msg neffos.Message) error {
		fmt.Printf("left %s\n", msg.Namespace)
		return nil
	}

	client, err := neffos.Dial(ctx, gorilla.Dialer(&gorilla.Options{}, header), addr, neffos.Namespaces{
		"chat": neffos.Events{"Say": print("%s\n"), neffos.OnNamespaceDisconnect: left},
		"news": neffos.Events{"Headline": print("headline: %s\n"), neffos.OnNamespaceDisconnect: left},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		fmt.Println("connection closed")
	}()

	chat, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}

	for line := range input(client) {
		switch line {
		case "":
		case "/quit":
			return
		case "/news":
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, err := client.Connect(ctx, "news")
			cancel()
			if err != nil {
				fmt.Printf("news: %v\n", err)
			}
		default:
			if !chat.Emit("Say", []byte(line)) {
				fmt.Println("not sent: you are not in chat")
			}
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
