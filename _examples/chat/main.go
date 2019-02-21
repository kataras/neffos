// Package main is a 1-1 chat example between a server and a client.
// Run:
// $ go run main.go server # and
// $ go run main.go client # at two different terminal tabs.
package main

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"

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
	ws := fastws.New()
	// true for continue, false for disconnect if the error came before connected.
	ws.OnError = func(c *fastws.Conn) bool {
		err := c.Err()
		if fastws.IsDisconnected(err) {
			log.Printf("[%s] remote connection has been manually closed", c.String())
			return false
		}

		log.Printf("[%s] Errored: %#+v", c.String(), err)
		return true
	}

	ws.OnConnected = func(c *fastws.Conn) error {
		go startTermWriter(c)
		return startReader(c)
	}

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	log.Fatal(http.ListenAndServe(endpoint, http.HandlerFunc(ws.UpgradeHTTP)))
}

func client() {
	c, err := fastws.Dial(nil, endpoint)
	if err != nil {
		log.Fatal(err)
	}

	go startTermWriter(c)
	err = startReader(c)
	if err != nil {
		if fastws.IsDisconnected(err) {
			log.Fatal("Remote server disconnected.")
		}
		log.Fatal(err)
	}
}

func startReader(c *fastws.Conn) error {
	// ------------
	// Reader part.
	// ------------
	//
	// Conn completes both io.Reader and io.Writer.

	for {
		message, err := c.ReadText()
		if err != nil {
			return err
		}

		// // silly example code for "command simulation", seperation of id and text.
		// received := strings.Split(scanner.Text(), "@")
		// if len(received) != 2 {
		// 	return errUnexpectedFormat
		// }
		// username, message := received[0], received[1]

		if c.ID == "" {
			c.ID = "server"
		}

		fmt.Printf("<%s>\t%s\n", c.ID, message)
	}
}

func startTermWriter(c *fastws.Conn) error {
	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Fprint(os.Stdout, ">> ")
		if !scanner.Scan() {
			return scanner.Err()
		}

		message := scanner.Text()
		if message == "" {
			continue
		}

		// ------------
		// Writer part.
		// ------------
		_, err := fmt.Fprint(c, message)
		if err != nil {
			return err
		}
	}
}
