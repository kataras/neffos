// A StackExchange of your own: two servers in one program, joined by an in-process bus.
//
// With a neffos.StackExchange set, a server stops delivering broadcasts by
// itself and hands them to the exchange, which carries them to every server
// that shares it, Redis or NATS in the ready-made ones. Writing one means
// eight methods. OnConnect and OnDisconnect add and remove a connection's
// own channel, for messages with Message.To. Subscribe and Unsubscribe follow
// the namespaces it enters and leaves. Publish carries messages to every
// subscriber. Ask and NotifyAsk carry Server.Ask across servers: the asking
// server waits on a token, the server that holds the target connection gets
// the client's reply and calls NotifyAsk with that token.
//
// Two optional interfaces complete it. StackExchangeInitializer's Init runs
// once, from Server.UseStackExchange, with the server's namespaces.
// StackExchangeCloser's Close runs from Server.Close and Server.Shutdown.
//
// The broker here is bus, a map of channels to connections in memory, so
// it only joins servers inside one process; that keeps the example free of
// any service while every method does real work. "server" starts two
// servers, on :8080 and :9090, each with its own exchange on the same bus.
// Chat crosses between them, and the operator console of the first one asks
// clients of the second with Server.Ask. main_test.go runs the same checks.
//
// Learn: implement StackExchange, StackExchangeInitializer and StackExchangeCloser, and see Server.Ask cross servers.
//
// Run:
//
//	go run main.go server                # terminal 1; logs "exchange a ready for namespaces [chat]" and the same for b
//	go run main.go client alice 8080     # terminal 2, on server a
//	go run main.go client bob 9090       # terminal 3, on server b
//
// Try:
//
//	> hello                        # bob's terminal; alice prints "bob: hello", from the other server
//	ping bob                       # the first server's terminal; logs "ping bob: pong from bob" through the bus
//	ping carol                     # logs "ping carol: context deadline exceeded" after three seconds
//	go test -v .                   # the same checks as a test
//
// Next: ../../08-load-testing/server measures a neffos server under many connections.
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

// bus is the broker: named channels with subscribed connections, and the
// waiting asks by token. Every exchange on the same bus sees every message.
type bus struct {
	mu   sync.Mutex
	subs map[string]map[*neffos.Conn]struct{} // channel name to subscribers
	asks map[string]chan []byte               // ask token to its reply
}

func newBus() *bus {
	return &bus{subs: map[string]map[*neffos.Conn]struct{}{}, asks: map[string]chan []byte{}}
}

func (b *bus) subscribe(channel string, c *neffos.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subs[channel] == nil {
		b.subs[channel] = map[*neffos.Conn]struct{}{}
	}
	b.subs[channel][c] = struct{}{}
}

func (b *bus) unsubscribe(channel string, c *neffos.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs[channel], c)
	if len(b.subs[channel]) == 0 {
		delete(b.subs, channel)
	}
}

// unsubscribeAll removes c from every channel, when it disconnects.
func (b *bus) unsubscribeAll(c *neffos.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for channel, conns := range b.subs {
		delete(conns, c)
		if len(conns) == 0 {
			delete(b.subs, channel)
		}
	}
}

// publish delivers payload to every subscriber of channel. Each connection
// decodes it and writes it to its client; neffos drops it there for a
// connection that is excluded, or not in the message's room.
func (b *bus) publish(channel string, payload []byte) {
	b.mu.Lock()
	conns := slices.Collect(maps.Keys(b.subs[channel]))
	b.mu.Unlock()

	for _, c := range conns {
		msg := c.DeserializeMessage(neffos.TextMessage, payload)
		msg.FromStackExchange = true
		c.Write(msg)
	}
}

// wait registers an ask token and returns where its reply will arrive.
func (b *bus) wait(token string) <-chan []byte {
	ch := make(chan []byte, 1)
	b.mu.Lock()
	b.asks[token] = ch
	b.mu.Unlock()
	return ch
}

func (b *bus) forget(token string) {
	b.mu.Lock()
	delete(b.asks, token)
	b.mu.Unlock()
}

// reply hands payload to the ask waiting on token, if it still waits.
func (b *bus) reply(token string, payload []byte) {
	b.mu.Lock()
	ch, ok := b.asks[token]
	b.mu.Unlock()
	if ok {
		select {
		case ch <- payload:
		default: // a reply already arrived
		}
	}
}

// exchange is one server's StackExchange on the bus.
type exchange struct {
	name string
	bus  *bus
}

var (
	_ neffos.StackExchange            = (*exchange)(nil)
	_ neffos.StackExchangeInitializer = (*exchange)(nil)
	_ neffos.StackExchangeCloser      = (*exchange)(nil)
)

func connChannel(id string) string      { return "conn." + id }
func namespaceChannel(ns string) string { return "namespace." + ns }
func channelOf(msg neffos.Message) string { // rooms travel on their namespace
	if msg.To != "" {
		return connChannel(msg.To)
	}
	return namespaceChannel(msg.Namespace)
}

// Init runs once, from Server.UseStackExchange.
func (e *exchange) Init(namespaces neffos.Namespaces) error {
	log.Printf("exchange %s ready for namespaces %v", e.name, slices.Sorted(maps.Keys(namespaces)))
	return nil
}

func (e *exchange) OnConnect(c *neffos.Conn) error {
	e.bus.subscribe(connChannel(c.ID()), c)
	return nil
}

func (e *exchange) OnDisconnect(c *neffos.Conn) {
	e.bus.unsubscribeAll(c)
}

func (e *exchange) Subscribe(c *neffos.Conn, namespace string) {
	e.bus.subscribe(namespaceChannel(namespace), c)
}

func (e *exchange) Unsubscribe(c *neffos.Conn, namespace string) {
	e.bus.unsubscribe(namespaceChannel(namespace), c)
}

func (e *exchange) Publish(msgs []neffos.Message) bool {
	for _, msg := range msgs {
		e.bus.publish(channelOf(msg), msg.Serialize())
	}
	return true
}

// Ask publishes msg and waits for the reply that NotifyAsk sends with token.
func (e *exchange) Ask(ctx context.Context, msg neffos.Message, token string) (neffos.Message, error) {
	replies := e.bus.wait(token)
	defer e.bus.forget(token)

	e.bus.publish(channelOf(msg), msg.Serialize())

	select {
	case <-ctx.Done():
		return neffos.Message{}, ctx.Err()
	case payload := <-replies:
		reply := neffos.DeserializeMessage(neffos.TextMessage, payload, false, false)
		return reply, reply.Err
	}
}

// NotifyAsk runs on the server that received the client's reply.
func (e *exchange) NotifyAsk(msg neffos.Message, token string) error {
	msg.ClearWait()
	e.bus.reply(token, msg.Serialize())
	return nil
}

// Close runs from Server.Close and Server.Shutdown. The bus belongs to the
// program, not to one server, so there is nothing to release here.
func (e *exchange) Close() error {
	log.Printf("exchange %s closed", e.name)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "server":
		runServers()
	case "client":
		if len(os.Args) < 3 {
			usage()
		}
		port := "8080"
		if len(os.Args) > 3 {
			port = os.Args[3]
		}
		runClient("ws://localhost:"+port+"/ws", os.Args[2])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go server | client <name> [8080|9090]")
	os.Exit(2)
}

var serverEvents = neffos.Namespaces{
	"chat": neffos.Events{
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			msg.Body = fmt.Appendf(nil, "%s: %s", c.Conn.ID(), msg.Body)
			c.BroadcastOthers(msg) // through the exchange, so both servers deliver it
			return nil
		},
	},
}

// newServer builds a server named name whose exchange is on b.
func newServer(name string, b *bus) *neffos.Server {
	srv := neffos.New(gorilla.DefaultUpgrader, serverEvents)
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("name")
	}
	srv.OnConnect = func(c *neffos.Conn) error {
		log.Printf("[%s] %s connected", name, c.ID())
		return nil
	}
	if err := srv.UseStackExchange(&exchange{name: name, bus: b}); err != nil {
		log.Fatal(err)
	}
	return srv
}

func runServers() {
	b := newBus()
	a := newServer("a", b)
	go serve(":8080", a)
	go serve(":9090", newServer("b", b))

	// The operator console belongs to server a; its Ask reaches clients of b.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		name, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "ping ")
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		reply, err := a.Ask(ctx, neffos.Message{To: name, Namespace: "chat", Event: "Ping"})
		cancel()
		if err != nil {
			log.Printf("ping %s: %v", name, err)
			continue
		}
		log.Printf("ping %s: %s", name, reply.Body)
	}
	select {} // keep serving after the console's input ends
}

func serve(addr string, srv *neffos.Server) {
	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// clientEvents prints chat and answers the operator's ping.
var clientEvents = neffos.Namespaces{
	"chat": neffos.Events{
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Println(string(msg.Body))
			return nil
		},
		"Ping": func(c *neffos.NSConn, msg neffos.Message) error {
			fmt.Println("* the operator pinged you")
			return neffos.Reply([]byte("pong from " + c.Conn.ID()))
		},
	},
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, clientEvents)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}

	for line := range input(client) {
		switch line {
		case "":
		case "/quit":
			return
		default:
			c.Emit("Chat", []byte(line))
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
