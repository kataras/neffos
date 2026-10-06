// Protocol Buffers bodies over neffos events.
//
// neffos never looks inside Message.Body, so any encoding works. Here every
// chat line is a UserMessage, defined in user_message.proto and generated
// into user_message.pb.go. The client encodes it with proto.Marshal and
// sends it with NSConn.EmitBinary, because protobuf output is binary and
// does not belong in a text frame. The server relays the bytes untouched
// with BroadcastOthers, and the other clients decode them with
// proto.Unmarshal.
//
// To regenerate the Go code after changing the .proto file:
//
//	protoc --go_out=. --go_opt=paths=source_relative user_message.proto
//
// Learn: carry Protocol Buffers messages in neffos bodies as binary frames.
//
// Requires: protoc and protoc-gen-go only to regenerate user_message.pb.go; running needs neither.
//
// Run:
//
//	go run . server                # terminal 1
//	go run . client alice          # terminal 2
//	go run . client bob            # terminal 3
//
// Try:
//
//	> hello                        # bob's terminal; alice prints "bob: hello"
//
// Next: ../native-messages talks to clients that send plain websocket frames.
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

	"google.golang.org/protobuf/proto"
)

const namespace = "chat"

var serverEvents = neffos.Namespaces{
	namespace: neffos.Events{
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] relays %d bytes, binary=%v", c.Conn.ID(), len(msg.Body), msg.SetBinary)
			c.BroadcastOthers(msg) // the body stays protobuf; SetBinary is kept
			return nil
		},
	},
}

var clientEvents = neffos.Namespaces{
	namespace: neffos.Events{
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			var m UserMessage
			if err := proto.Unmarshal(msg.Body, &m); err != nil {
				return err
			}
			fmt.Printf("%s: %s\n", m.Username, m.Text)
			return nil
		},
	},
}

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
		runClient("ws://localhost:8080/ws", os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run . server | client <name>")
	os.Exit(2)
}

func runServer(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/ws", neffos.New(gorilla.DefaultUpgrader, serverEvents))

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr, clientEvents)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	c, err := client.Connect(ctx, namespace)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}

		body, err := proto.Marshal(&UserMessage{Username: name, Text: text})
		if err != nil {
			log.Fatal(err)
		}
		c.EmitBinary("Chat", body)
	}
}
