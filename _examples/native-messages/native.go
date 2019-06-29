package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// HOW TO RUN
//
// $ go run native.go server
// #
// # open a browser tab and navigate to http://localhost:8080 and write something to the text input.
// # in the same time you can also run the go client
// # which is created using neffos but the standard go websocket library could be used instead as well.
// #
// $ go run native.go client
func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		panic("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	switch side {
	case "server":
		runServer()
	case "client":
		runClient()
	default:
		fmt.Printf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
		os.Exit(1)
	}
}

var pongMessage = []byte("pong")

var events = neffos.Events{
	neffos.OnNativeMessage: func(c *neffos.NSConn, msg neffos.Message) error {
		log.Printf("Got: %s", string(msg.Body))

		if !c.Conn.IsClient() {
			return c.Conn.Socket().WriteText(pongMessage, 0)
		}

		return nil
	},
}

func runServer() {
	websocketServer := neffos.New(
		gorilla.DefaultUpgrader, events)

	router := http.NewServeMux()
	router.Handle("/endpoint", websocketServer)
	router.Handle("/", http.FileServer(http.Dir("./browser")))

	log.Println("Serving websockets on localhost:8080/endpoint")
	log.Fatal(http.ListenAndServe(":8080", router))
}

func runClient() {
	ctx := context.Background()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, "ws://localhost:8080/endpoint", events)
	if err != nil {
		panic(err)
	}

	// Connect does nothing at this case,
	// it just returns a connection to work with its methods.
	c, err := client.Connect(ctx, "")
	if err != nil {
		panic(err)
	}

	c.Conn.Socket().WriteText([]byte("ping"), 0)

	<-client.NotifyClose
}
