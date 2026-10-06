// One user, many connections: grouping devices by user.
//
// A person with a phone and a laptop open has two connections. neffos gives
// every connection its own ID, so this example makes the ID "name#n" in
// Server.IDGenerator (alice#1, alice#2) and keeps a small registry that maps
// a user name to the set of their *neffos.NSConn values. OnNamespaceConnected
// adds a device and OnNamespaceDisconnect removes it; the registry knows when
// a user's first device arrives and when the last one leaves, which is when
// "online" and "offline" really change.
//
// A message to a user goes to every device of that user, and a copy goes to
// the sender's other devices, so a conversation reads the same on all of
// them. A handler error tells the sender when nobody is there.
//
// Learn: track the connections of one user and send to all of them.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2, alice's laptop
//	go run main.go client alice    # terminal 3, alice's phone
//	go run main.go client bob      # terminal 4
//
// Try:
//
//	> alice hi                     # bob's terminal; both alice terminals print "bob: hi"
//	> bob hello back               # one alice terminal; bob and the other alice terminal print "alice: hello back"
//	> carol hi                     # "not sent: carol is offline"
//
// Next: ../rate-limit counts per-connection events and closes abusive connections.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

const namespace = "chat"

// devices maps a user name to the connections that user has open.
type devices struct {
	mu    sync.RWMutex
	conns map[string]map[*neffos.NSConn]struct{}
}

// add registers c for user and reports whether it is the user's first device.
func (d *devices) add(user string, c *neffos.NSConn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	set := d.conns[user]
	if set == nil {
		set = make(map[*neffos.NSConn]struct{})
		d.conns[user] = set
	}
	set[c] = struct{}{}
	return len(set) == 1
}

// remove forgets c and reports whether it was the user's last device.
func (d *devices) remove(user string, c *neffos.NSConn) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	delete(d.conns[user], c)
	if len(d.conns[user]) > 0 {
		return false
	}
	delete(d.conns, user)
	return true
}

// emit sends to every device of user except skip and returns how many it reached.
func (d *devices) emit(user string, skip *neffos.NSConn, event string, body []byte) int {
	d.mu.RLock()
	defer d.mu.RUnlock()

	n := 0
	for c := range d.conns[user] {
		if c != skip && c.Emit(event, body) {
			n++
		}
	}
	return n
}

var registry = &devices{conns: make(map[string]map[*neffos.NSConn]struct{})}

// userOf returns the user part of a "name#n" connection ID.
func userOf(c *neffos.NSConn) string {
	user, _, _ := strings.Cut(c.Conn.ID(), "#")
	return user
}

var serverEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			if registry.add(userOf(c), c) {
				log.Printf("%s is online (%s)", userOf(c), c.Conn.ID())
			} else {
				log.Printf("%s opened another device (%s)", userOf(c), c.Conn.ID())
			}
			return nil
		},
		neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
			if registry.remove(userOf(c), c) {
				log.Printf("%s is offline", userOf(c))
			}
			return nil
		},
		// "Tell" carries "<user> <text>".
		"Tell": func(c *neffos.NSConn, msg neffos.Message) error {
			to, text, _ := strings.Cut(string(msg.Body), " ")
			line := []byte(userOf(c) + ": " + text)
			if registry.emit(to, nil, "Message", line) == 0 {
				return fmt.Errorf("%s is offline", to) // the sender gets it as Message.Err
			}
			registry.emit(userOf(c), c, "Message", line) // a copy for the sender's other devices
			return nil
		},
	},
}

var clientEvents = neffos.Namespaces{
	namespace: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf("connected as %s; type \"<user> <text>\"\n", c.Conn.ID())
			return nil
		},
		"Message": func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Printf("%s\n", msg.Body)
			return nil
		},
		"Tell": func(c *neffos.NSConn, msg neffos.Message) error {
			if msg.Err != nil {
				fmt.Printf("not sent: %v\n", msg.Err)
			}
			return nil
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

func newServer() *neffos.Server {
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)

	// Every connection gets its own ID, "name#n", so devices never collide.
	var seq atomic.Uint64
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		name := r.Header.Get("X-Username")
		if name == "" {
			name = r.URL.Query().Get("name")
		}
		if name == "" {
			name = "guest"
		}
		return fmt.Sprintf("%s#%d", name, seq.Add(1))
	}

	return srv
}

func runServer(addr string) {
	mux := http.NewServeMux()
	mux.Handle("/ws", newServer())

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	dialer := gorilla.Dialer(&gorilla.Options{}, http.Header{"X-Username": {name}})
	client, err := neffos.Dial(ctx, dialer, addr, clientEvents)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	c, err := client.Connect(ctx, namespace)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			c.Emit("Tell", []byte(line))
		}
	}
}
