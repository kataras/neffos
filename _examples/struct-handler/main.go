package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
)

type serverConn struct {
	// dynamic field, a new "serverConn" instance is created
	// on each new connection to this namespace.
	Conn *neffos.NSConn
	// a static field is allowed, if filled before server ran then
	// is set on each new "serverConn" instance.
	SuffixResponse string
}

func (c *serverConn) Namespace() string {
	return "default"
}

func (c *serverConn) OnChat(msg neffos.Message) error {
	c.Conn.Emit("ChatResponse", append(msg.Body, []byte(c.SuffixResponse)...))
	return nil
}

// This is supported too, uncomment these lines,
// import the std "errors" package and run a client.
//
// func (c *serverConn) OnNamespaceConnect(msg neffos.Message) error {
// 	return errors.New("not allowed")
// }

func (c *serverConn) OnNamespaceConnected(msg neffos.Message) error {
	log.Printf("[%s] connected to namespace [%s]", c.Conn, msg.Namespace)
	return nil
}

func (c *serverConn) OnNamespaceDisconnect(msg neffos.Message) error {
	log.Printf("[%s] disconnected from namespace [%s]", c.Conn, msg.Namespace)
	return nil
}

type clientConn struct {
	Conn *neffos.NSConn
}

func (s *clientConn) ChatResponse(msg neffos.Message) error {
	log.Printf("[%s] Echo back from server: %s", s.Conn, string(msg.Body))
	return nil
}

// $ go run main.go server
// $ go run main.go client
// # expected output:
// # Echo back from server: Hello from client!Static Response Suffix for sake of the example.
func main() {
	neffos.EnableDebug(nil)

	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	switch side {
	case "server":
		startServer()
	case "client":
		startClient()
	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}

}

func startServer() {
	controller := new(serverConn)
	controller.SuffixResponse = "Static Response Suffix for sake of the example"

	// This will convert a structure to neffos.Namespaces based on the struct's methods.
	// The methods can be func(msg neffos.Message) error if the structure contains a *neffos.NSConn field,
	// otherwise they should be like any event callback: func(nsConn *neffos.NSConn, msg neffos.Message) error.
	// If contains a field of type *neffos.NSConn then a new controller
	// is created on each new connection to this namespace
	// and static fields(if any) are set on runtime with the NSConn itself.
	// If it's a static controller (does not contain a NSConn field)
	// then it just registers its functions as regular events without performance cost.
	events := neffos.NewStruct(controller).
		// Optionally, sets read and write deadlines on the underlying network connection.
		// After a read or write have timed out, the websocket connection is closed.
		// For example:
		// If a client or server didn't receive or sent something
		// for 20 seconds this connection will be terminated.
		SetTimeouts(20*time.Second, 20*time.Second).
		// This will convert the "OnChat" method to a "Chat" event instead.
		SetEventMatcher(neffos.EventTrimPrefixMatcher("On"))

	websocketServer := neffos.New(gobwas.DefaultUpgrader, events)

	log.Println("Listening on: ws://localhost:8080\nPress CTRL/CMD+C to interrupt.")
	log.Fatal(http.ListenAndServe(":8080", websocketServer))
}

func startClient() {
	controller := new(clientConn)
	events := neffos.NewStruct(controller).
		// This sets a namespace.
		// Alternatively you can add a `Namespace() string`
		// as you've seen on the `serverConn struct` above
		// or leave it empty for empty namespace.
		SetNamespace("default")

	client, err := neffos.Dial(nil, gobwas.DefaultDialer, "ws://localhost:8080", events)
	if err != nil {
		panic(err)
	}

	c, err := client.Connect(nil, "default")
	if err != nil {
		panic(err)
	}

	c.Emit("Chat", []byte("Hello from client!"))
	<-client.NotifyClose
}
