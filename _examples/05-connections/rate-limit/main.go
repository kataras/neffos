// A per-connection message limit that closes a flooding client with a code.
//
// Every connection has a small store of its own (Conn.Set and Conn.Get),
// and Conn.Increment and Conn.Decrement change an integer in it safely from
// any goroutine. That is enough for a sliding window: each chat message
// increments "recent" and schedules a Decrement one second later, so
// "recent" is always the number of messages from the last second. Above
// five, the handler returns neffos.CloseError{Code:
// neffos.ClosePolicyViolation, Reason: "rate limit"}. neffos sends the error
// to the client as usual and then closes the connection with code 1008 and
// that reason, which the client reads from Client.Conn().Err().
//
// Learn: keep per-connection counters with Increment and Decrement and close an abusive connection with a code and a reason.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> /flood 5                     # bob's terminal; alice prints "bob: flood 1" to "bob: flood 5", within the limit
//	> /flood 10                    # bob prints "refused: [1008] rate limit" and "connection closed: 1008 rate limit"
//	                               # alice gets five more lines, then "* bob left"
//	                               # the server logs "[bob] closed: 6 messages in the last 1s"
//
// Next: ../inspect-and-do lists, inspects and acts on live connections.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const (
	limit  = 5           // messages
	window = time.Second // per this long
)

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
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name>")
	os.Exit(2)
}

var serverEvents = neffos.Namespaces{
	"chat": neffos.Events{
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			c.BroadcastOthers(neffos.Message{Namespace: "chat", Event: "Say", Body: []byte("* " + c.Conn.ID() + " left")})
			return nil
		},
		"Say": func(c *neffos.NSConn, msg neffos.Message) error {
			if n := c.Conn.Increment("recent"); n > limit {
				log.Printf("[%s] closed: %d messages in the last %s", c.Conn.ID(), n, window)
				return neffos.CloseError{Code: neffos.ClosePolicyViolation, Reason: "rate limit"}
			}
			time.AfterFunc(window, func() { c.Conn.Decrement("recent") })

			msg.Body = fmt.Appendf(nil, "%s: %s", c.Conn.ID(), msg.Body)
			c.BroadcastOthers(msg)
			return nil
		},
	},
}

func runServer(addr string) {
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("name")
	}

	http.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws; limit: %d messages per %s", addr, limit, window)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, neffos.Namespaces{
		"chat": neffos.Events{
			"Say": func(c *neffos.NSConn, msg neffos.Message) error {
				if msg.Err != nil { // the server refused one of our own messages
					fmt.Printf("refused: %v\n", msg.Err)
					return nil
				}
				fmt.Println(string(msg.Body))
				return nil
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close() // does nothing if the server closed it first
		err := client.Conn().Err()
		if code := neffos.CloseStatus(err); code != neffos.CloseNormalClosure {
			ce, _ := errors.AsType[neffos.CloseError](err)
			fmt.Printf("connection closed: %d %s\n", code, ce.Reason)
			return
		}
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}

	for line := range input(client) {
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "":
		case "/quit":
			return
		case "/flood": // /flood 10 sends ten lines at once
			n, _ := strconv.Atoi(arg)
			for i := range n {
				c.Emit("Say", fmt.Appendf(nil, "flood %d", i+1))
			}
		default:
			c.Emit("Say", []byte(line))
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
