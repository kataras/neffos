// Upgraders and dialers built with options, the same policy on all three backends.
//
// The Default* values of every backend package use the library's defaults.
// For anything else, build the library's own options struct and hand it to
// the package's Upgrader or Dialer function. This example sets the same
// three things on each backend, so you can compare them side by side:
//
//   - The origin policy. A page from another site must not open a websocket
//     that carries your users' cookies, so only pages from this host and
//     from https://app.example.com get in; everything else gets 403.
//     gorilla takes a CheckOrigin function and coder takes OriginPatterns.
//     gobwas has no origin hook, so a small HTTP middleware does it.
//   - A subprotocol, "lobby.v1", offered by the dialer and accepted by the
//     upgrader, which is how a client and a server agree on a wire format.
//   - Handshake details: buffer sizes and timeouts where the library has
//     them, and an X-Username request header set through the dialer's own
//     header option (gorilla.Dialer's second argument, gobwas.Header,
//     coder's HTTPHeader).
//
// Learn: configure each backend's upgrader and dialer through its own options type.
//
// Run:
//
//	go run main.go -backend coder server          # terminal 1; or gorilla, or gobwas
//	go run main.go -backend gobwas client alice   # terminal 2; any backend
//
// Try:
//
//	> hello                        # alice's terminal prints "server: alice said hello"
//	curl -si -m 1 -H "Origin: https://evil.example" -H "Connection: Upgrade" -H "Upgrade: websocket" -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" localhost:8080/ws
//	                               # HTTP/1.1 403 Forbidden
//	curl -si -m 1 -H "Origin: https://app.example.com" -H "Sec-WebSocket-Protocol: lobby.v1" -H "Connection: Upgrade" -H "Upgrade: websocket" -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" localhost:8080/ws
//	                               # HTTP/1.1 101 Switching Protocols, with "Sec-WebSocket-Protocol: lobby.v1"
//
// Next: ../socket-wrapper wraps the Socket a backend returns to watch every frame.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
	gobwasws "github.com/gobwas/ws"
	gorillaws "github.com/gorilla/websocket"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/coder"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

const (
	subprotocol   = "lobby.v1"
	trustedOrigin = "app.example.com" // besides pages served by this host
)

// allowedOrigin is the policy: no Origin (not a browser), the same host, or
// the trusted one.
func allowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && (u.Host == r.Host || u.Host == trustedOrigin)
}

// upgrader returns the server side of backend with the options above, and a
// wrapper for the handler, which only gobwas needs.
func upgrader(backend string) (neffos.Upgrader, func(http.Handler) http.Handler) {
	none := func(h http.Handler) http.Handler { return h }

	switch backend {
	case "gorilla":
		return gorilla.Upgrader(gorillaws.Upgrader{
			HandshakeTimeout: 5 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			Subprotocols:     []string{subprotocol},
			CheckOrigin:      allowedOrigin,
		}), none
	case "gobwas":
		return gobwas.Upgrader(gobwasws.HTTPUpgrader{
			Timeout:  5 * time.Second,
			Protocol: func(p string) bool { return p == subprotocol },
		}), checkOrigin
	default: // coder
		// Pages from this host are always allowed; the patterns add others.
		return coder.Upgrader(coderws.AcceptOptions{
			Subprotocols:   []string{subprotocol},
			OriginPatterns: []string{trustedOrigin},
		}), none
	}
}

// checkOrigin applies allowedOrigin before the upgrade, for gobwas.
func checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedOrigin(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// dialer returns the client side of backend, sending name in X-Username.
func dialer(backend, name string) neffos.Dialer {
	header := http.Header{"X-Username": {name}}

	switch backend {
	case "gorilla":
		return gorilla.Dialer(&gorilla.Options{
			HandshakeTimeout: 5 * time.Second,
			ReadBufferSize:   4096,
			WriteBufferSize:  4096,
			Subprotocols:     []string{subprotocol},
		}, header)
	case "gobwas":
		return gobwas.Dialer(gobwas.Options{
			Timeout:   5 * time.Second,
			Protocols: []string{subprotocol},
			Header:    gobwas.Header(header),
		})
	default: // coder
		// coder bounds the handshake with the context given to Dial.
		return coder.Dialer(&coder.Options{
			Subprotocols: []string{subprotocol},
			HTTPHeader:   header,
		})
	}
}

func main() {
	backend := flag.String("backend", "gorilla", "websocket library: gorilla, gobwas or coder")
	flag.Parse()

	if !slices.Contains([]string{"gorilla", "gobwas", "coder"}, *backend) || flag.NArg() < 1 {
		usage()
	}

	switch flag.Arg(0) {
	case "server":
		runServer(":8080", *backend)
	case "client":
		if flag.NArg() < 2 {
			usage()
		}
		runClient("ws://localhost:8080/ws", *backend, flag.Arg(1))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run main.go [-backend gorilla|gobwas|coder] server | client <name>")
	os.Exit(2)
}

// events serves both sides: the server answers Echo, the client prints it.
var events = neffos.Namespaces{
	"chat": neffos.Events{
		"Echo": func(c *neffos.NSConn, msg neffos.Message) error {
			if c.Conn.IsClient() {
				fmt.Println(string(msg.Body))
				return nil
			}
			log.Printf("[%s] %s", c.Conn.ID(), msg.Body)
			return neffos.Reply(fmt.Appendf(nil, "server: %s said %s", c.Conn.ID(), msg.Body))
		},
	},
}

func runServer(addr, backend string) {
	up, wrap := upgrader(backend)
	srv := neffos.New(up, events)
	srv.IDGenerator = func(w http.ResponseWriter, r *http.Request) string {
		if name := r.Header.Get("X-Username"); name != "" {
			return name
		}
		return neffos.DefaultIDGenerator(w, r)
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("upgrade failed: %v", err)
	}

	http.Handle("/ws", wrap(srv))
	log.Printf("%s server listening on %s, websocket endpoint /ws", backend, addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func runClient(addr, backend, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, dialer(backend, name), addr, events)
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
			c.Emit("Echo", []byte(line))
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
