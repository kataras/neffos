package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/kataras/fastws"
)

const endpoint = "localhost:8080"

var (
	// Events holds the registered connection's events.
	// key = event, value = connections listening to,
	// the default event for each connection is its ID,
	// therefore each new connection has an event with its own ID filled here.
	Events = make(map[string][]*fastws.Conn)
	// The "defaultEvent" is one more default event that all connections are subscribed automatically too,
	// so they can share messages.
	defaultEvent = "public"
	mu           sync.RWMutex // locker for `events`.
)

func put(c *fastws.Conn) {
	mu.Lock()
	Events[c.ID] = []*fastws.Conn{c}
	mu.Unlock()
}

// remove "c" from all events.
func del(c *fastws.Conn) {
	mu.Lock()
	for evt, conns := range Events {
		for i, conn := range conns {
			if c.ID == conn.ID {
				Events[evt] = append(conns[:i], conns[i+1:]...)
				break // break "conns".
			}
		}
	}
	mu.Unlock()
}

func subscribe(c *fastws.Conn, event string) {
	mu.RLock()
	conns := Events[event]
	mu.RUnlock()

	for _, conn := range conns { // always non-nil because it defaults to itself.
		if c.ID == conn.ID {
			// already subscribed.
			return
		}
	}

	mu.Lock()
	Events[event] = append(conns, c)
	mu.Unlock()
}

func unsubscribe(c *fastws.Conn, event string) {
	mu.RLock()
	conns := Events[event]
	mu.RUnlock()

	for i := range conns {
		if c.ID == conns[i].ID {
			conns = append(conns[:i], conns[i+1:]...)
			mu.Lock()
			Events[event] = conns
			mu.Unlock()
			break
		}
	}
}

func broadcast(c *fastws.Conn, msg publishMessage) (err error) {
	msg.From = c.ID

	mu.RLock()
	defer mu.RUnlock()

	conns := Events[msg.Event]
	for _, conn := range conns {
		if conn.ID == c.ID { // except "myself".
			continue
		}

		err = conn.Encode(msg)
		if err != nil {
			return err
		}
	}

	return
}

// when someone publishes to event, they subscribed automatically, for the shake of simplicity.
type publishMessage struct {
	From  string `json:"from"`  // the connection id.
	Event string `json:"event"` // the event to publish to.
	Text  string `json:"text"`  // the message text.
}

func (msg publishMessage) String() string {
	return fmt.Sprintf("<%s> %s", msg.From, msg.Text)
}

// type subscribeMessage struct {
// 	ConnID string `json:"connID"` // the connection id.
// 	Event  string `json:"event"`  // the event to listen to.
// }

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
	ws.OnError = func(c *fastws.Conn) bool {
		err := c.Err()
		// if err `IsDisconnected`, then it is unexpected disconnection from the client-side.
		if closed := fastws.IsDisconnected(err); closed {
			del(c)
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

		put(c)
		defer del(c)

		// subscribe to the default, the public one automatically.
		subscribe(c, defaultEvent)

		var msg publishMessage

		for {
			err := c.Decode(&msg)
			if err != nil {
				if !ws.HandleError(c, err) {
					return err
				}
				continue
				// Or break and disconnect by firing the "OnError" with the "err" automatically:
				// return err
			}

			err = broadcast(c, msg)
			if err != nil {
				if !ws.HandleError(c, err) {
					return err
				}
				continue
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
