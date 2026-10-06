// neffos inside an Iris application, behind a JWT check.
//
// Iris v12 ships a websocket package built on neffos: websocket.Handler(srv)
// turns a *neffos.Server into an Iris handler, so it takes part in a
// route's middleware chain like any other handler, and it must come last.
// Here the chain starts with the verifier of Iris's middleware/jwt.
// jwt.NewVerifier reads the token from the Authorization header or from
// ?token= (browsers cannot set handshake headers), answers 401 when it is
// missing, forged or expired, and stores the verified claims on the request.
// No websocket is opened for a bad token.
//
// Inside neffos, websocket.GetContext(conn) returns the Iris context of the
// handshake request, and jwt.Get(ctx) the claims verified for it. The
// connection ID comes from them through an Iris websocket.IDGenerator, and
// the chat events read the user the same way, so a client cannot claim to
// be someone else. GET /token?name=alice signs a short-lived token for
// alice: a stand-in for a real login, which the Go client calls first.
//
// This example is its own Go module, see its go.mod, because it follows the
// public Iris release. The websocket package of Iris v12.2.11 tracks neffos
// v0.0.x until the next Iris release, so this program builds against that
// neffos version and not against the code in the parent directory.
//
// Learn: mount a neffos server on an Iris route behind middleware and read the verified user in events.
//
// Run:
//
//	go run main.go server          # terminal 1
//	go run main.go client alice    # terminal 2
//	go run main.go client bob      # terminal 3
//
// Try:
//
//	> hello                        # bob's terminal; alice prints "bob: hello"
//	curl -i localhost:8080/ws                     # 401 Unauthorized, before any upgrade
//	curl -i "localhost:8080/ws?token=forged"      # 401 Unauthorized
//	curl -s "localhost:8080/token?name=carol"     # prints a token for carol, valid for 15 minutes
//
// Next: this is the last example; the wiki (https://github.com/kataras/neffos/wiki) goes deeper into each topic.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kataras/iris/v12"
	"github.com/kataras/iris/v12/middleware/jwt"
	"github.com/kataras/iris/v12/websocket"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// secret signs and verifies the tokens. A real app loads it from its configuration.
var secret = []byte("change me: a long random secret")

// userClaims is what a token says about its holder.
type userClaims struct {
	Username string `json:"username"`
}

// userOf returns the verified user of a websocket connection.
func userOf(c *neffos.Conn) string {
	claims, _ := jwt.Get(websocket.GetContext(c)).(*userClaims)
	if claims == nil {
		return ""
	}
	return claims.Username
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
		runClient("localhost:8080", os.Args[2])
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
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			token := jwt.GetVerifiedToken(websocket.GetContext(c.Conn))
			expires := time.Unix(token.StandardClaims.Expiry, 0).Format(time.TimeOnly)
			log.Printf("[%s] entered chat, token valid until %s", userOf(c.Conn), expires)
			return nil
		},
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			msg.Body = fmt.Appendf(nil, "%s: %s", userOf(c.Conn), msg.Body)
			c.Conn.Server().Broadcast(c, msg) // everyone in the namespace but the sender
			return nil
		},
	},
}

func runServer(addr string) {
	signer := jwt.NewSigner(jwt.HS256, secret, 15*time.Minute)
	verifier := jwt.NewVerifier(jwt.HS256, secret) // reads the header, then ?token=

	ws := neffos.New(gorilla.DefaultUpgrader, serverEvents)

	app := iris.New()
	// A stand-in for a login: sign a token for any name.
	app.Get("/token", func(ctx iris.Context) {
		name := ctx.URLParam("name")
		if name == "" {
			ctx.StopWithText(iris.StatusBadRequest, "name is required")
			return
		}
		token, err := signer.Sign(userClaims{Username: name})
		if err != nil {
			ctx.StopWithError(iris.StatusInternalServerError, err)
			return
		}
		ctx.Write(token)
	})
	// The verifier runs first; websocket.Handler is the last handler of the route.
	app.Get("/ws",
		verifier.Verify(func() any { return new(userClaims) }),
		websocket.Handler(ws, func(ctx iris.Context) string {
			return jwt.Get(ctx).(*userClaims).Username // the connection ID
		}),
	)

	log.Fatal(app.Listen(addr))
}

func runClient(host, name string) {
	// Log in first: here, ask the server to sign a token for name.
	resp, err := http.Get("http://" + host + "/token?name=" + url.QueryEscape(name))
	if err != nil {
		log.Fatal(err)
	}
	token, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		log.Fatalf("token: %s %s", resp.Status, token)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	header := http.Header{"Authorization": {"Bearer " + string(token)}}
	client, err := neffos.Dial(ctx, gorilla.Dialer(&gorilla.Options{}, header), "ws://"+host+"/ws", neffos.Namespaces{
		"chat": neffos.Events{
			"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
				fmt.Println(string(msg.Body))
				return nil
			},
		},
	})
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
	fmt.Printf("signed in as %s\n", c.Conn.ID())

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
