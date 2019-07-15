package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwasalive"
)

var events = neffos.WithTimeout{
	ReadTimeout:  60 * time.Second,
	WriteTimeout: 60 * time.Second,

	Namespaces: neffos.Namespaces{
		"v1": neffos.Events{
			"echo": onEcho,
		},
	},
}

func onEcho(c *neffos.NSConn, msg neffos.Message) error {
	body := string(msg.Body)
	log.Println(body)

	if !c.Conn.IsClient() {
		newBody := append([]byte("echo back: "), msg.Body...)
		return neffos.Reply(newBody)
	}

	return nil
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		log.Fatalf("expected program to start with 'server' or 'client' argument")
	}
	side := args[0]

	clientNum := 0
	if len(args) == 2 {
		clientNum, _ = strconv.Atoi(args[1])
		log.Printf("Start clientNum: %v", clientNum)
	}

	switch side {
	case "server":
		runServer()
	case "client":
		if clientNum == 0 {
			runClient()
		} else {
			runClientMany(clientNum)
		}

	default:
		log.Fatalf("unexpected argument, expected 'server' or 'client' but got '%s'", side)
	}
}

func runServer() {
	upgrader := gobwasalive.NewUpgrader(50 * time.Second)
	websocketServer := neffos.New(upgrader, events)

	websocketServer.OnConnect = func(c *neffos.Conn) error {
		log.Printf("OnConnect cid: %v", c.ID())
		return nil
	}

	websocketServer.OnDisconnect = func(c *neffos.Conn) {
		log.Printf("OnDisconnect cid: %v", c.ID())
	}

	router := http.NewServeMux()
	router.Handle("/echo", websocketServer)

	ticker := time.NewTicker(30 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Printf("total clients: %v", websocketServer.GetTotalConnections())
			}
		}
	}()

	log.Println("Serving websockets on localhost:8080/echo")
	log.Fatal(http.ListenAndServe(":8080", router))
}

func runClient() {
	ctx := context.Background()
	dialer := gobwasalive.NewDialer(45 * time.Second)
	client, err := neffos.Dial(ctx, dialer, "ws://localhost:8080/echo", events)
	if err != nil {
		panic(err)
	}

	_, err = client.Connect(ctx, "v1")
	if err != nil {
		panic(err)
	}

	//c.Emit("echo", []byte("Greetings!"))

	// a channel that blocks until client is terminated,
	// i.e by CTRL/CMD +C.
	<-client.NotifyClose
}

func runClientMany(clientNum int) {
	for i := 0; i < clientNum; i++ {
		time.Sleep(5 * time.Millisecond)
		go runClient()
	}
	ch := make(chan os.Signal)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	log.Println(<-ch)
}
