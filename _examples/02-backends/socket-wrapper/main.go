// Wrapping the Socket of every connection to count its frames.
//
// A backend's Upgrader returns a neffos.Socket, the small interface neffos
// reads and writes through. Server.Upgrade(w, r, wrapper, nil) does what
// ServeHTTP does, but first passes that Socket to wrapper, so you can put
// your own type in between: here countingSocket counts the frames and bytes
// in each direction. The counts include neffos's own messages, such as the
// handshake and the namespace connect, not only the events you emit. Upgrade
// also returns the *neffos.Conn, and Conn.Socket() hands the wrapper back.
//
// Embedding neffos.Socket only exposes the five Socket methods. neffos looks
// for three optional interfaces on the Socket: SocketCloser (close frames with
// a code), SocketPinger (the heartbeat) and SocketReadLimiter
// (MaxMessageSize). A wrapper that does not pass them on silently turns those
// features off, so countingSocket implements all three by delegation. The
// server sets a heartbeat and a 1 KB limit to show they still work.
//
// Learn: put your own Socket between neffos and the backend without losing its optional features.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//
// Try:
//
//	> hello                        # alice's terminal prints "echo: hello"
//	> /stats                       # prints "in: 4 frames, 115 bytes; out: 3 frames, 69 bytes" (the server's view)
//	> /big                         # a 2 KB message; prints "connection closed: 1009", the limit set through the wrapper
//	                               # the server logs "[alice] closed (message too big: ...) after 4 frames in, 4 frames out"
//
// Next: ../../03-messaging/protobuf sends Protocol Buffers bodies in binary frames.
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// countingSocket counts the frames and bytes that pass through a Socket.
type countingSocket struct {
	neffos.Socket // the backend's socket; ReadData and Write* are overridden

	framesIn, bytesIn   atomic.Int64
	framesOut, bytesOut atomic.Int64
}

func (s *countingSocket) ReadData(timeout time.Duration) ([]byte, neffos.MessageType, error) {
	body, typ, err := s.Socket.ReadData(timeout)
	if err == nil {
		s.framesIn.Add(1)
		s.bytesIn.Add(int64(len(body)))
	}
	return body, typ, err
}

func (s *countingSocket) WriteText(body []byte, timeout time.Duration) error {
	s.countOut(body)
	return s.Socket.WriteText(body, timeout)
}

func (s *countingSocket) WriteBinary(body []byte, timeout time.Duration) error {
	s.countOut(body)
	return s.Socket.WriteBinary(body, timeout)
}

func (s *countingSocket) countOut(body []byte) {
	s.framesOut.Add(1)
	s.bytesOut.Add(int64(len(body)))
}

func (s *countingSocket) String() string {
	return fmt.Sprintf("in: %d frames, %d bytes; out: %d frames, %d bytes",
		s.framesIn.Load(), s.bytesIn.Load(), s.framesOut.Load(), s.bytesOut.Load())
}

// The optional interfaces, passed on to the backend's socket. gorilla, gobwas
// and coder implement all three; the fallbacks are for a Socket that does not.

func (s *countingSocket) Close(code int, reason string, timeout time.Duration) error {
	if closer, ok := s.Socket.(neffos.SocketCloser); ok {
		return closer.Close(code, reason, timeout)
	}
	return s.NetConn().Close()
}

func (s *countingSocket) Ping(timeout time.Duration) error {
	if pinger, ok := s.Socket.(neffos.SocketPinger); ok {
		return pinger.Ping(timeout)
	}
	return nil // no way to ping: report success so the heartbeat does not close
}

func (s *countingSocket) SetReadLimit(n int64) {
	if limiter, ok := s.Socket.(neffos.SocketReadLimiter); ok {
		limiter.SetReadLimit(n)
	}
}

var (
	_ neffos.SocketCloser      = (*countingSocket)(nil)
	_ neffos.SocketPinger      = (*countingSocket)(nil)
	_ neffos.SocketReadLimiter = (*countingSocket)(nil)
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

func runServer(addr string) {
	srv := neffos.New(gorilla.DefaultUpgrader, neffos.WithTimeout{
		PingInterval:   10 * time.Second,
		MaxMessageSize: 1 << 10,
		Namespaces: neffos.Namespaces{
			"chat": neffos.Events{
				"Echo": func(c *neffos.NSConn, msg neffos.Message) error {
					return neffos.Reply(append([]byte("echo: "), msg.Body...))
				},
				"Stats": func(c *neffos.NSConn, msg neffos.Message) error {
					return neffos.Reply([]byte(c.Conn.Socket().(*countingSocket).String()))
				},
			},
		},
	})
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		return r.URL.Query().Get("name")
	}
	srv.OnDisconnect = func(c *neffos.Conn) {
		s := c.Socket().(*countingSocket)
		log.Printf("[%s] closed (%v) after %d frames in, %d frames out",
			c.ID(), c.Err(), s.framesIn.Load(), s.framesOut.Load())
	}

	wrap := func(s neffos.Socket) neffos.Socket { return &countingSocket{Socket: s} }
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := srv.Upgrade(w, r, wrap, nil) // nil: keep the server's IDGenerator
		if err != nil {
			log.Printf("upgrade failed: %v", err)
			return
		}
		log.Printf("[%s] connected through %T", c.ID(), c.Socket())
	})

	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(addr, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, addr+"?name="+name, neffos.Namespaces{
		"chat": neffos.Events{},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		client.Close()
		if code := neffos.CloseStatus(client.Conn().Err()); code != neffos.CloseNormalClosure {
			fmt.Printf("connection closed: %d\n", code)
			return
		}
		fmt.Println("connection closed")
	}()

	c, err := client.Connect(ctx, "chat")
	if err != nil {
		log.Fatal(err)
	}

	// ask sends an event and prints the answer.
	ask := func(event string, body []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		reply, err := c.Ask(ctx, event, body)
		if err != nil {
			fmt.Printf("%s: %v\n", event, err)
			return
		}
		fmt.Println(string(reply.Body))
	}

	for line := range input(client) {
		switch line {
		case "":
		case "/quit":
			return
		case "/stats":
			ask("Stats", nil)
		case "/big":
			c.Emit("Echo", []byte(strings.Repeat("x", 2<<10)))
		default:
			ask("Echo", []byte(line))
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
