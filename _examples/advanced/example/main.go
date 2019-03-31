package main

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/fastws/_examples/advanced/ws"
)

const endpoint = "localhost:8080"

var handler = ws.Namespaces{
	"default": ws.Events{
		ws.OnNamespaceConnect: func(c ws.NSConn, msg ws.Message) error {
			log.Printf("[%s] connected to [%s].", c.ID(), msg.Namespace)
			return nil
		},
		ws.OnNamespaceDisconnect: func(c ws.NSConn, msg ws.Message) error {
			log.Printf("[%s] disconnected from [%s].", c.ID(), msg.Namespace)
			return nil
		},
		"chat": func(c ws.NSConn, msg ws.Message) error {
			if !c.IsClient() {
				log.Printf("--server-side-- send back the message [%s:%s]", msg.Event, string(msg.Body))
				c.Emit(msg.Event, msg.Body)
			} else {
				if bytes.Equal(msg.Body, []byte("exit")) {
					c.Disconnect()
					return nil
				}
			}

			log.Printf("---------------------\n[%s] %s", c.ID(), msg.Body)

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
		server()
	case "client":
		client()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func server() {
	srv := ws.New(handler)
	// srv.SetIDGenerator(func(r *http.Request) string {
	// 	return "my-user"
	// })

	srv.OnConnect = func(c ws.Conn) error {
		log.Printf("[%s] connected to server.", c.ID())
		return nil
	}
	srv.OnDisconnect = func(c ws.Conn) {
		log.Printf("[%s] disconnected from the server.", c.ID())
	}
	srv.OnError = func(c ws.Conn, err error) bool {
		log.Printf("ERROR: [%s] %v", c.ID(), err)
		return false
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	http.ListenAndServe(endpoint, srv)
}

func client() {
	client, err := ws.Dial(nil, endpoint, handler)
	if err != nil {
		panic(err)
	}

	c := client.Connect("default")

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()

		// if bytes.Equal(text, []byte("exit")) {
		// 	c.Disconnect()
		// 	return
		// }

		fmt.Fprint(os.Stdout, ">> ")
		c.Emit("chat", text)
	}

}
