// Native messages: plain websocket frames, no neffos protocol.
//
// A neffos server can talk to clients that know nothing about neffos: a
// browser's own WebSocket, websocat, or any other websocket library. Register
// only the empty namespace with a single neffos.OnNativeMessage event and the
// connection switches to native mode: every incoming frame arrives on that
// event with the raw frame in Message.Body, and a Message with IsNative set
// goes out as a plain frame, with no namespace or event around it. There is
// no namespace to connect to; the empty one is ready as soon as the socket
// is.
//
// The page served at / uses the browser's WebSocket directly. The Go client
// below uses neffos with the same events, which puts it in native mode too.
//
// Learn: serve clients that speak plain websocket with OnNativeMessage and Message.IsNative.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client          # terminal 2, or open http://localhost:8080
//
// Try:
//
//	> ping                         # the client prints "server: pong to ping"
//	websocat ws://localhost:8080/ws    # then type hello; websocat prints "pong to hello"
//
// Next: ../known-errors shares error values between server and client.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

//go:embed index.html
var page []byte

// events has only the empty namespace and only OnNativeMessage, which is
// what puts both sides in native mode.
var events = neffos.Events{
	neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
		if c.Conn.IsClient() {
			fmt.Printf("server: %s\n", msg.Body)
			return nil
		}

		log.Printf("[%s] sent %q", c.Conn.ID(), msg.Body)
		c.Conn.Write(neffos.Message{IsNative: true, Body: []byte("pong to " + string(msg.Body))})
		return nil
	},
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run main.go server | client")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "server":
		runServer(":8080")
	case "client":
		runClient("ws://localhost:8080/ws")
	}
}

func runServer(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/ws", neffos.New(gorilla.DefaultUpgrader, events))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})

	log.Printf("listening on %s: open http://localhost%s, websocket endpoint /ws", addr, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(addr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr, events)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	// In native mode Connect does no handshake; it returns the empty namespace.
	c, err := client.Connect(ctx, "")
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			c.Conn.Write(neffos.Message{IsNative: true, Body: []byte(line)})
		}
	}
}
