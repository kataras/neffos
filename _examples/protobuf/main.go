package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"

	"github.com/golang/protobuf/proto"
)

//
// Generate proto for Go:
// protoc --go_out=. user_message.proto
// go build
//
// Windows
// protobuf.exe server# once
// protobuf.exe client # two or more times
//
// Unix
// ./protobuf server # once
// ./protobuf client # two or more times
//
// At short, the `Message.Body` is the raw data client/server send,
// users of this library can use any format to unmarshal on read and marshal to send;
// protocolbuffers, encoding/json, encoding/xml and etc.
//
//
// Browser support:
// https://github.com/protocolbuffers/protobuf/tree/master/js#commonjs-imports
// https://github.com/protobufjs/protobuf.js#nodejs (with browserify, we use that in ./browser example)
// https://github.com/protobufjs/protobuf.js#browsers (alternative)
//
// Generate proto for JavaScript:
// protoc --js_out=import_style=commonjs,binary:./browser user_message.proto
// See `./browser/app.js` for more.
//
const (
	addr      = "localhost:8080"
	endpoint  = "/echo"
	namespace = "default"
)

var serverAndClientEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			// ready to send data to this namespace.

			log.Printf("[%s] connected to [%s].\n", c, msg.Namespace)

			// if is not client-side and returns a
			// non-nil error then it refuses the client to connect to this specific namespace.
			return nil
		},
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			log.Printf("[%s] disconnected from [%s].\n", c, msg.Namespace)
			return nil
		},
		"chat": func(c *neffos.NSConn, msg neffos.Message) error {
			if msg.Err != nil {
				log.Printf("remote error: %v from [%s]\n", msg.Err, c)
				return nil
			}

			if !c.Conn.IsClient() {
				// fmt.Printf("[%s] sending message to everyone... [%v:%s]\n", c, msg.SetBinary, string(msg.Body))
				// broadcast to all clients except this one, when first parameter is not nil.
				c.Conn.Server().Broadcast(c, msg)
			} else {
				// client received from server's broadcast.
				var userMsg UserMessage

				if err := proto.Unmarshal(msg.Body, &userMsg); err != nil {
					fmt.Printf("[example] error on proto.Unmarshal: %v\nFor msg.Body equals to: %s", err, string(msg.Body))
					return err
				}
				fmt.Printf("[%s] says: %s\n", userMsg.Username, userMsg.Text)
			}

			// if returns an error then the remote side's `msg.Err` will be filled with
			// this error's text.
			return nil
		},
		"chat_test": func(c *neffos.NSConn, msg neffos.Message) error {
			if !c.Conn.IsClient() {
				c.Conn.Server().Broadcast(c, msg)
				return nil
			}

			fmt.Printf("[%s] says: %s\n", c, string(msg.Body))
			return nil
		},
	},
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	switch side {
	case "server":
		startServer()
	case "client":
		startClient()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func startServer() {
	server := neffos.New(gorilla.DefaultUpgrader, serverAndClientEvents)
	server.OnConnect = func(c *neffos.Conn) error {
		log.Printf("[%s] connected to the server.", c)

		// if returns non-nil error then it refuses the client to connect to the server.
		return nil
	}
	server.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("[%s] disconnected from the server.", c)
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", addr)
	// log.Fatal(http.ListenAndServe(addr, server))
	http.Handle("/", http.FileServer(http.Dir("./browser")))
	http.Handle(endpoint, server)
	log.Fatal(http.ListenAndServe(addr, nil))
}

const testHelloBinaryWithSeps = false

func startClient() {
	// init the websocket connection by dialing the server.
	client, err := neffos.Dial(nil, gorilla.DefaultDialer, addr+endpoint, serverAndClientEvents)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		<-client.NotifyClose
		os.Exit(0)
	}()

	// connect to the "default" namespace.
	c, err := client.Connect(nil, namespace)
	if err != nil {
		log.Fatal(err)
	}

	if testHelloBinaryWithSeps {
		c.Conn.Write(neffos.Message{
			Namespace: namespace,
			Event:     "chat_test",
			Body:      []byte{';', ';', ';', ';', 'h', 'e', 'l', 'l', 'o', ';'},
			SetBinary: true,
		})
	}

	fmt.Fprintf(os.Stdout, "Please specify a username: ")
	usernameBytes, _, _ := bufio.NewReader(os.Stdin).ReadLine()
	userMsg := &UserMessage{
		Username: string(usernameBytes),
		// only `Text` field is dynamic, therefore we can reuse this instance value,
		// the `Text` field can be filled right before the `c#EmitBinary`, check below.
	}

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()

		if bytes.Equal(text, []byte("exit")) {
			client.Close() // or c.Conn.Close(), it's exactly the same.
			// or to disconnect from the specific namespace:
			// if err := c.Disconnect(nil); err != nil {
			// 	log.Printf("ERROR: %v", err)
			// }

			break
		}

		// send data to the "chat" event.
		userMsg.Text = string(text)
		body, err := proto.Marshal(userMsg)
		if err != nil {
			log.Fatal(err)
		}

		c.EmitBinary("chat", body)

		fmt.Fprint(os.Stdout, ">> ")
	}
}
