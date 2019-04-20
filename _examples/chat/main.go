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
	ws := fastws.New()
	ws.OnConnected = func(c *fastws.Conn) error {
		// true for continue, false for disconnect if the error came before connected.
		c.OnError = func(err error) bool {
			if fastws.IsDisconnected(err) {
				log.Printf("[%s] remote connection has been manually closed", c.String())
				return false
			}

			log.Printf("[%s] Errorred: %#+v", c.String(), err)
			return true
		}

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

const idleTimeout = 4 * time.Second

func startReader(c *fastws.Conn) error {
	// ------------
	// Reader part.
	// ------------
	//
	// Conn completes both io.Reader and io.Writer.

	// ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	// defer cancel()

	in := make(chan []byte, 256)

	go func() {
		idle := time.NewTimer(idleTimeout)
		defer idle.Stop()
		for {
			select {
			case <-idle.C:
				if !c.IsClient() {
					fmt.Printf("<%s> idle client\n", c.ID)
					c.NetConn.Close()
				}
				return
			case data := <-in:
				idle.Reset(idleTimeout)
				message := string(data)

				if c.ID == "" {
					c.ID = "server"
				}

				fmt.Printf("<%s>\t%s\n", c.ID, message)
			}
		}
	}()

	for {
		data, err := c.ReadBinary()
		if err != nil {
			return err
		}

		in <- data
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
