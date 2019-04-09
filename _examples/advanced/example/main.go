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
			if msg.Err != nil {
				log.Printf("This client can't connect because of: %v", msg.Err)
				return nil
			}

			err := fmt.Errorf("Server says that you are not allowed here")
			/* comment this to see that the server-side will
			no allow to for this socket to be connected to the "default" namespace
			and an error will be logged to the client. */
			err = nil
			if err == nil {
				log.Printf("[%s] connected to [%s].", c.ID(), msg.Namespace)
			}

			return err
		},
		ws.OnNamespaceDisconnect: func(c ws.NSConn, msg ws.Message) error {
			if msg.Err != nil {
				log.Printf("This client can't disconnect yet, server does not allow that action, reason: %v", msg.Err)
				return nil
			}

			err := fmt.Errorf("Server says that you are not allowed to be disconnected yet")
			/* here if you comment this, the return error will mean that
			the disconnect message from client-side will be ignored from the server
			and the connection would be still available to send message to the "default" namespace
			it will not be disconnected.*/
			err = nil

			if err == nil {
				log.Printf("[%s] disconnected from [%s].", c.ID(), msg.Namespace)
			}

			return err
		},
		"chat": func(c ws.NSConn, msg ws.Message) error {
			if !c.IsClient() {
				// this is possible too:
				// if bytes.Equal(msg.Body, []byte("force disconnect")) {
				// 	println("force disconnect")
				// 	return c.Disconnect()
				// }

				log.Printf("--server-side-- send back the message [%s:%s]", msg.Event, string(msg.Body))
				c.Emit(msg.Event, msg.Body)
			}

			log.Printf("---------------------\n[%s] %s", c.ID(), msg.Body)
			return nil
		},
	},
}

const namespace = "default"

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
	go http.ListenAndServe(endpoint, srv)

	fmt.Fprint(os.Stdout, ">> ")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		if !scanner.Scan() {
			log.Printf("ERROR: %v", scanner.Err())
			return
		}

		text := scanner.Bytes()
		if bytes.Equal(text, []byte("force disconnect")) {
			// for _, conn := range srv.GetConnectionsByNamespace(namespace) {
			// 	conn.Disconnect()
			// }
			// srv.Broadcast(nil, ws.Message{
			// 	Namespace: namespace,
			// 	Event:     ws.OnNamespaceDisconnect,
			// })
			srv.Do(func(c ws.Conn) {
				c.Close()
				// c.DisconnectFrom(namespace)
			})
		} else {
			srv.Do(func(c ws.Conn) {
				c.Write(namespace, "chat", text)
			})
		}
		fmt.Fprint(os.Stdout, ">> ")
	}
}

func client() {
	client, err := ws.Dial(nil, endpoint, handler)
	if err != nil {
		panic(err)
	}
	// defer client.Close()

	c, err := client.Connect(namespace)
	if err != nil {
		panic(err)
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
			if err := c.Disconnect(); err != nil {
				log.Printf("from server: %v", err)
			}
			continue
		}

		ok := c.Emit("chat", text)
		if !ok {
			break
		}

		fmt.Fprint(os.Stdout, ">> ")
	}

}
