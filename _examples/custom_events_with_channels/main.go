// Package main shows a simple case of events/rooms to chat between clients using channels.
// Run:
// $ go build && ./custom_events_with_channels server # x1
// $ ./custom_events_with_channels client # x2
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kataras/fastws"
)

const endpoint = "localhost:8080"

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
	bus := &eventBus{
		events:      make(map[string][]*fastws.Conn),
		broadcastCh: make(chan publishMessage),
		actionCh:    make(chan action),
		errorCh:     make(chan error),
	}

	go bus.start()

	ws := fastws.New()
	ws.OnError = func(c *fastws.Conn) bool {
		err := c.Err()
		// if err `IsDisconnected`, then it is unexpected disconnection from the client-side.
		if closed := fastws.IsDisconnected(err); closed {
			return false
		}

		log.Printf("[%s] Errored: %v", c.String(), err)
		return true
	}

	// For expected disconnection, from server-side.
	// Fire any error on `OnConnected` exit to fire it.
	ws.OnDisconnected = func(c *fastws.Conn) {
		log.Printf("[%s] Disconnected.", c.String())
	}

	// fired before Upgrade,
	// can be used for custom checks and for customization of the Conn's properties.
	// if returns no error then the `OnConnected` is fired when connection is established.
	ws.OnUpgrade = func(c *fastws.Conn) error {
		// [...]
		return nil
	}

	ws.OnConnected = func(c *fastws.Conn) error {
		c.SetEncoding(json.NewEncoder(c), json.NewDecoder(c))

		bus.put(c)
		defer bus.del(c)

		// subscribe to the default, the public one automatically.
		bus.subscribe(c, defaultEvent)

		var msg publishMessage

		for {
			select {
			case err := <-bus.errorCh:
				if !ws.HandleError(c, err) {
					return err
				}
			default:
				err := c.Decode(&msg)
				if err != nil {
					if !ws.HandleError(c, err) {
						return err
					}
					continue
					// Or break and disconnect by firing the "OnError" with the "err" automatically:
					// return err
				}

				msg.From = c.ID // server-side "from" set for "security".
				bus.broadcast(msg)
			}
		}
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	log.Fatal(http.ListenAndServe(endpoint, http.HandlerFunc(ws.UpgradeHTTP)))
}

func client() {
	// exit after 30 seconds for the shake of the example,
	// ofc this is not for production, you can put `context.WithTimeout` there.
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
	defer cancel()

	c, err := fastws.Dial(ctx, endpoint)
	if err != nil {
		log.Fatal(err)
	}

	// c.ReadTimeout = 15 * time.Second
	// c.WriteTimeout = 15 * time.Second
	c.Reader.CheckUTF8 = true

	c.SetEncoding(json.NewEncoder(c), json.NewDecoder(c))

	go startClientPublisher(c)
	err = startClientSubscriber(c)

	if fastws.IsTimeout(err) {
		log.Println("Your time is up. This client has been expired after 30 seconds!")
	} else if fastws.IsClosed(err) {
		log.Fatal("Remote server closed. Exiting...")
	}
}

func startClientSubscriber(c *fastws.Conn) error {
	var msg publishMessage

	for {
		err := c.Decode(&msg)
		if err != nil {
			if fastws.IsDisconnected(err) {
				return err
			}
			log.Printf("Errrored: %#+v", err)
			continue
		}

		fmt.Println(msg.String())
	}
}

func startClientPublisher(c *fastws.Conn) error {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print(">> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		message := scanner.Text()
		if message == "" {
			continue
		}

		err := c.Encode(publishMessage{
			From:  c.ID,
			Event: defaultEvent,
			Text:  message,
		})

		if err != nil {
			log.Println(err)
		}
	}

}
