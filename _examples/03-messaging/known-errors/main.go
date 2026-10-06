// Errors that keep their identity across the wire, and one that closes the connection.
//
// An event handler that returns an error sends its text to the other side.
// By default that side gets errors.New(text), a new value nothing can match.
// neffos.RegisterKnownError, called on both sides before any connection,
// makes the receiving side return the registered value instead, so the
// client can test it with errors.Is. errEmpty is matched by its exact text.
// errTooLong is a type with a ResolveError(text) method: the server wraps it
// with the numbers ("too long: 140 characters, the limit is 100"), the
// method matches any text with that start, and the client gets errTooLong
// back. The client gets the registered value, not the server's text, so data
// the client must read belongs in a reply body, not in the error.
//
// A handler can also end the connection: returning
// neffos.CloseError{Code: 4001, Reason: "mind your language"} sends the error
// as usual and then closes with that code and reason. The client reads both
// from Client.Conn().Err() with neffos.CloseStatus once NotifyClose fires.
//
// Learn: share error values between server and client, and close a connection from an event with a code.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//
// Try:
//
//	> hello                        # prints "posted"
//	> /empty                       # posts an empty message; prints "not posted: say something" (errors.Is errEmpty)
//	> /long                        # posts 140 characters; prints "not posted: keep it under 100 characters" (errors.Is errTooLong)
//	> darn it                      # prints "post: [4001] mind your language", then "connection closed: 4001 mind your language"
//
// Next: ../../04-handlers/compose-handlers builds handlers from pieces.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const (
	maxLength      = 100
	closeLanguage  = 4001 // an application close code: 4000 to 4999 are free
	languageReason = "mind your language"
)

// errEmpty is matched by its text, which never changes.
var errEmpty = errors.New("empty message")

// tooLongError is matched by ResolveError, because its text carries numbers.
type tooLongError struct{}

func (tooLongError) Error() string { return "too long" }

// ResolveError reports whether text came from this error on the other side.
func (tooLongError) ResolveError(text string) bool {
	return strings.HasPrefix(text, "too long")
}

var errTooLong error = tooLongError{}

// Both sides run this program, so one init registers the errors on both.
func init() {
	neffos.RegisterKnownError(errEmpty)
	neffos.RegisterKnownError(errTooLong)
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
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name>")
	os.Exit(2)
}

var serverEvents = neffos.Namespaces{
	"chat": neffos.Events{
		"Post": func(c *neffos.NSConn, msg neffos.Message) error {
			text := string(msg.Body)
			n := utf8.RuneCountInString(text)
			switch {
			case n == 0:
				return errEmpty
			case n > maxLength:
				return fmt.Errorf("%w: %d characters, the limit is %d", errTooLong, n, maxLength)
			case strings.Contains(text, "darn"):
				log.Printf("[%s] closed: %s", c.Conn.ID(), languageReason)
				return neffos.CloseError{Code: closeLanguage, Reason: languageReason}
			}
			log.Printf("[%s] posted %q", c.Conn.ID(), text)
			return neffos.Reply([]byte("posted"))
		},
	},
}

func runServer(addr string) {
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("name")
	}

	http.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, neffos.Namespaces{"chat": neffos.Events{}})
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
		switch line {
		case "/quit":
			return
		case "/empty":
			line = ""
		case "/long":
			line = strings.Repeat("a", 140)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		reply, err := c.Ask(ctx, "Post", []byte(line))
		cancel()
		switch {
		case errors.Is(err, errEmpty):
			fmt.Println("not posted: say something")
		case errors.Is(err, errTooLong):
			fmt.Printf("not posted: keep it under %d characters\n", maxLength)
		case err != nil:
			fmt.Printf("post: %v\n", err)
		default:
			fmt.Println(string(reply.Body))
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
