// Building the server's handlers from pieces, and a catch-all event.
//
// One big neffos.Namespaces literal is fine for a small app. A bigger one
// grows its handlers in separate pieces, often one per feature, and joins
// them at the end. Events.On adds one event to an Events value, and
// Namespaces.On adds one event to a namespace, creating the namespace when
// needed and returning its Events. neffos.JoinConnHandlers merges any number
// of pieces into one handler: the events of a namespace that appears in
// several pieces are combined, and on a name clash the later piece wins.
//
// Here chat builds the "chat" namespace with Events.On, presence adds its
// lifecycle events to the same namespace with Namespaces.On, admin adds a
// second namespace, and fallback adds neffos.OnAnyEvent, which runs for any
// event of "chat" that no piece declared. That includes lifecycle events
// such as OnNamespaceConnect, so fallback lets those through with
// neffos.IsSystemEvent; returning an error there would refuse the
// connection. The server logs the merged result when it starts.
//
// Learn: compose handlers with Events.On, Namespaces.On and JoinConnHandlers, and catch unknown events with OnAnyEvent.
//
// Run:
//
//	go run main.go server          # terminal 1; logs "admin: Count" and "chat: Say, Shout, _OnAnyEvent, ..."
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> hello                        # bob's terminal; alice prints "bob: hello"
//	> /shout lunch                 # alice prints "bob shouts: LUNCH"
//	> /count                       # prints "2 people in chat", answered by the admin namespace
//	> /dance                       # prints "dance: no event "Dance" in chat", answered by OnAnyEvent
//
// Next: ../struct-injector builds per-connection controllers with their dependencies.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// chat is the chat feature, built one event at a time with Events.On.
func chat() neffos.Namespaces {
	events := neffos.Events{}
	events.On("Say", func(c *neffos.NSConn, msg neffos.Message) error {
		msg.Body = fmt.Appendf(nil, "%s: %s", c.Conn.ID(), msg.Body)
		c.BroadcastOthers(msg)
		return nil
	})
	events.On("Shout", func(c *neffos.NSConn, msg neffos.Message) error {
		msg.Body = fmt.Appendf(nil, "%s shouts: %s", c.Conn.ID(), strings.ToUpper(string(msg.Body)))
		c.BroadcastOthers(msg)
		return nil
	})
	return neffos.Namespaces{"chat": events}
}

// presence adds lifecycle logs to the same namespace with Namespaces.On.
func presence() neffos.Namespaces {
	nss := neffos.Namespaces{}
	nss.On("chat", neffos.OnNamespaceConnected, func(c *neffos.NSConn, msg neffos.Message) error {
		log.Printf("[%s] entered chat", c.Conn.ID())
		return nil
	})
	nss.On("chat", neffos.OnNamespaceDisconnect, func(c *neffos.NSConn, msg neffos.Message) error {
		log.Printf("[%s] left chat", c.Conn.ID())
		return nil
	})
	return nss
}

// admin is a second namespace with a question about the first.
func admin() neffos.Namespaces {
	nss := neffos.Namespaces{}
	nss.On("admin", "Count", func(c *neffos.NSConn, msg neffos.Message) error {
		n := len(c.Conn.Server().GetConnectionsByNamespace("chat"))
		return neffos.Reply(fmt.Appendf(nil, "%d people in chat", n))
	})
	return nss
}

// fallback answers every chat event that no other piece declared.
var fallback = neffos.Namespaces{
	"chat": neffos.Events{
		neffos.OnAnyEvent: func(c *neffos.NSConn, msg neffos.Message) error {
			// It also runs for lifecycle events no piece declared, such as
			// OnNamespaceConnect; an error there would refuse the connection.
			if neffos.IsSystemEvent(msg.Event) {
				return nil
			}
			log.Printf("[%s] sent the unknown event %q", c.Conn.ID(), msg.Event)
			return fmt.Errorf("no event %q in chat", msg.Event)
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
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name>")
	os.Exit(2)
}

func runServer(addr string) {
	handler := neffos.JoinConnHandlers(chat(), presence(), admin(), fallback)

	nss := handler.GetNamespaces()
	for _, ns := range slices.Sorted(maps.Keys(nss)) {
		log.Printf("%s: %s", ns, strings.Join(slices.Sorted(maps.Keys(nss[ns])), ", "))
	}

	srv := neffos.New(gorilla.DefaultUpgrader, handler)
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("name")
	}

	http.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// print shows the body of a message the server relayed.
func print(c *neffos.NSConn, msg neffos.Message) error {
	fmt.Println(string(msg.Body))
	return nil
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, neffos.Namespaces{
		"chat":  neffos.Events{"Say": print, "Shout": print},
		"admin": neffos.Events{},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		fmt.Println("connection closed")
	}()

	chat, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}
	admin, err := client.Connect(ctx, "admin")
	if err != nil {
		log.Fatal(err)
	}

	// ask asks ns and prints the answer or the error.
	ask := func(ns *neffos.NSConn, event string) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		reply, err := ns.Ask(ctx, event, nil)
		if err != nil {
			fmt.Printf("%s: %v\n", strings.ToLower(event), err)
			return
		}
		fmt.Println(string(reply.Body))
	}

	for line := range input(client) {
		cmd, arg, _ := strings.Cut(line, " ")
		switch cmd {
		case "":
		case "/quit":
			return
		case "/shout":
			chat.Emit("Shout", []byte(arg))
		case "/count":
			ask(admin, "Count")
		case "/dance": // no piece declares it
			ask(chat, "Dance")
		default:
			chat.Emit("Say", []byte(line))
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
