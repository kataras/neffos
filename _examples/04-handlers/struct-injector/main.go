// Controllers built by your own code, with their dependencies, one per connection.
//
// A struct with a *neffos.NSConn field is a per-connection controller:
// neffos.NewStruct makes a new value for every connection that enters its
// namespace. By default that value is a copy of the one you passed, which
// only works for plain fields. Struct.SetInjector hands the construction to
// you: the function receives the connection and returns a pointer to a value
// it built, here with a shared store and an audit log. neffos then fills the
// NSConn field, which must be exported.
//
// Two controllers, two namespaces: notes keeps each user's notes, audit
// reports what happened across all users. SetEventMatcher picks which
// methods are events. neffos.EventPrefixMatcher("Note") registers NoteAdd
// and NoteList under their own names and ignores every other method, unlike
// EventTrimPrefixMatcher, which also renames them. neffos.JoinConnHandlers
// mounts both controllers on one server.
//
// Learn: build per-connection controllers with SetInjector and choose their events with EventPrefixMatcher.
//
// Run:
//
//	go run main.go server          # terminal 1; logs "notes events: NoteAdd, NoteList, ..." and "audit events: AuditRecent, ..."
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> buy milk                     # alice's terminal; prints "note 1 saved"
//	> /list                        # prints "1. buy milk"; bob's /list prints "no notes", the store is per user
//	> /audit                       # any terminal; prints "alice opened their notes", "alice added note 1" and so on
//
// Next: ../../05-connections/users-and-devices groups the connections of one user.
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
	"sync"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// noteStore keeps every user's notes. All connections share one.
type noteStore struct {
	mu    sync.Mutex
	notes map[string][]string
}

func (s *noteStore) add(user, text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes[user] = append(s.notes[user], text)
	return len(s.notes[user])
}

func (s *noteStore) list(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.notes[user])
}

// auditLog remembers recent actions. All connections share one.
type auditLog struct {
	mu    sync.Mutex
	lines []string
}

func (a *auditLog) add(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, fmt.Sprintf(format, args...))
}

func (a *auditLog) recent(n int) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.lines[max(0, len(a.lines)-n):])
}

// notes handles the "notes" namespace for one connection.
type notes struct {
	Conn *neffos.NSConn // filled by neffos after the injector returns

	store *noteStore
	audit *auditLog
}

func (n *notes) user() string { return n.Conn.Conn.ID() }

func (n *notes) OnNamespaceConnected(msg neffos.Message) error {
	n.audit.add("%s opened their notes", n.user())
	return nil
}

func (n *notes) NoteAdd(msg neffos.Message) error {
	i := n.store.add(n.user(), string(msg.Body))
	n.audit.add("%s added note %d", n.user(), i)
	return neffos.Reply(fmt.Appendf(nil, "note %d saved", i))
}

func (n *notes) NoteList(msg neffos.Message) error {
	return neffos.ReplyObject(n.store.list(n.user()))
}

// audit handles the "audit" namespace for one connection.
type audit struct {
	Conn *neffos.NSConn

	log *auditLog
}

func (a *audit) AuditRecent(msg neffos.Message) error {
	return neffos.ReplyObject(a.log.recent(5))
}

// newHandler builds both controllers around the shared dependencies.
func newHandler(store *noteStore, log *auditLog) (*neffos.Struct[notes], *neffos.Struct[audit]) {
	notesController := neffos.NewStruct(&notes{}).
		SetNamespace("notes").
		SetEventMatcher(neffos.EventPrefixMatcher("Note")).
		SetInjector(func(_ *neffos.NSConn) *notes {
			return &notes{store: store, audit: log}
		})

	auditController := neffos.NewStruct(&audit{}).
		SetNamespace("audit").
		SetEventMatcher(neffos.EventPrefixMatcher("Audit")).
		SetInjector(func(_ *neffos.NSConn) *audit {
			return &audit{log: log}
		})

	return notesController, auditController
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
	notesController, auditController := newHandler(
		&noteStore{notes: map[string][]string{}},
		&auditLog{},
	)
	log.Printf("notes events: %s", strings.Join(slices.Sorted(maps.Keys(notesController.Events())), ", "))
	log.Printf("audit events: %s", strings.Join(slices.Sorted(maps.Keys(auditController.Events())), ", "))

	srv := neffos.New(gorilla.DefaultUpgrader, neffos.JoinConnHandlers(notesController, auditController))
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

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, neffos.Namespaces{
		"notes": neffos.Events{},
		"audit": neffos.Events{},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		fmt.Println("connection closed")
	}()

	notes, err := client.Connect(ctx, "notes")
	if err != nil {
		log.Fatal(err)
	}
	audit, err := client.Connect(ctx, "audit")
	if err != nil {
		log.Fatal(err)
	}

	// ask sends an event and returns the answer, or prints the error.
	ask := func(ns *neffos.NSConn, event, body string) (neffos.Message, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		reply, err := ns.Ask(ctx, event, []byte(body))
		if err != nil {
			fmt.Printf("%s: %v\n", event, err)
			return reply, false
		}
		return reply, true
	}

	for line := range input(client) {
		switch line {
		case "":
		case "/quit":
			return
		case "/list":
			if reply, ok := ask(notes, "NoteList", ""); ok {
				list, _ := reply.As[[]string]()
				if len(list) == 0 {
					fmt.Println("no notes")
				}
				for i, note := range list {
					fmt.Printf("%d. %s\n", i+1, note)
				}
			}
		case "/audit":
			if reply, ok := ask(audit, "AuditRecent", ""); ok {
				lines, _ := reply.As[[]string]()
				fmt.Println(strings.Join(lines, "\n"))
			}
		default:
			if reply, ok := ask(notes, "NoteAdd", line); ok {
				fmt.Println(string(reply.Body))
			}
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
