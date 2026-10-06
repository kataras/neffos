// Load test, client side: many short-lived Go clients against ../server.
//
// The program opens -clients connections, a few milliseconds apart, with a
// short garbage collection pause now and then so the client machine keeps
// up. Each client enters the empty namespace, emits every line of test.data
// as a "chat" event and closes after four, six or eight seconds, so
// connections come and go while others are still busy. -max limits how many
// are open at once (0 means no limit). At the end it prints the failures,
// grouped, or "all ok".
//
// The backend is a command-line argument, so the same test can compare
// gorilla, gobwas and coder. Both programs must get the same -clients.
//
// Learn: drive a neffos server with many concurrent Go clients and collect their errors.
//
// Requires: ../server running, and an operating system tuned for many sockets when -clients is large (see ../server).
//
// Run:
//
//	go run main.go -clients 1000 gobwas    # after ../server started with the same values
//
// Try:
//
//	go run main.go -clients 1000 -max 100 coder    # never more than 100 connections at once
//
// Next: ../broadcast measures one message to many clients instead.
package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"log"
	"math/rand/v2"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/coder"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

const (
	url     = "ws://localhost:9595/ws"
	verbose = false
)

var (
	testdata []byte
	// slots limits the open connections when -max is set; nil means no limit.
	slots chan struct{}

	totalConnectedNamespace atomic.Uint64

	errMu         sync.Mutex
	connectErrors []error
	enterErrors   []error
)

var handler = neffos.WithTimeout{
	ReadTimeout:  60 * time.Second,
	WriteTimeout: 60 * time.Second,
	Events: neffos.Events{
		neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
			totalConnectedNamespace.Add(1)
			return nil
		},
		"chat": func(c *neffos.NSConn, msg neffos.Message) error {
			if verbose {
				log.Println(string(msg.Body))
			}
			return nil
		},
	},
}

func main() {
	totalClients := flag.Int("clients", 100000, "how many clients to open; must match ../server")
	maxOpen := flag.Int("max", 0, "how many connections may be open at once, 0 for no limit")
	flag.Parse()

	dialer := gobwas.DefaultDialer
	switch backend := flag.Arg(0); backend {
	case "gorilla":
		dialer = gorilla.DefaultDialer
	case "coder":
		dialer = coder.DefaultDialer
	case "", "gobwas":
	default:
		log.Fatalf("unknown backend %q, expected gobwas, gorilla or coder", backend)
	}
	if *maxOpen > 0 {
		slots = make(chan struct{}, *maxOpen)
	}

	var err error
	testdata, err = os.ReadFile("test.data")
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("opening %d clients", *totalClients)
	start := time.Now()
	stopMonitor := startMonitor()

	var wg sync.WaitGroup
	relax := 15 * time.Millisecond // gives a modest client machine time to breathe
	lastRelax := time.Now()
	for i := range *totalClients {
		if time.Since(lastRelax) > relax {
			runtime.GC()
			time.Sleep(relax)
			lastRelax = time.Now()
		}
		if slots != nil {
			slots <- struct{}{}
		}

		alive := 8 * time.Second
		switch {
		case i%2 == 0:
			alive = 4 * time.Second
			time.Sleep(time.Duration(rand.IntN(6)) * time.Millisecond)
		case i%3 == 0:
			alive = 6 * time.Second
			time.Sleep(time.Duration(rand.IntN(6)) * time.Millisecond)
		}
		alive -= time.Duration(rand.IntN(3)) * time.Millisecond

		wg.Go(func() { connect(dialer, alive) })
	}

	wg.Wait()
	stopMonitor()

	log.Printf("execution time %s", time.Since(start))
	report(*totalClients)
}

func startMonitor() (stop func()) {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for range ticker.C {
			log.Printf("entered the namespace so far: %d", totalConnectedNamespace.Load())
		}
	}()
	return ticker.Stop
}

func connect(dialer neffos.Dialer, alive time.Duration) {
	if slots != nil {
		defer func() { <-slots }()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, dialer, url, handler)
	if err != nil {
		collect(&connectErrors, err)
		return
	}
	defer client.Close()

	ctx, cancel = context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	c, err := client.Connect(ctx, "")
	if err != nil {
		collect(&enterErrors, err)
		return
	}

	scanner := bufio.NewScanner(bytes.NewReader(testdata))
	for scanner.Scan() {
		if text := scanner.Bytes(); len(text) > 1 {
			if !c.Emit("chat", text) {
				if verbose {
					log.Printf("emit failed: the connection closed before the data was written")
				}
				return
			}
		}
	}

	time.Sleep(alive)
}

func collect(list *[]error, err error) {
	errMu.Lock()
	*list = append(*list, err)
	errMu.Unlock()
}

// report prints the failures, folding repeats of the same error into one line.
func report(totalClients int) {
	errMu.Lock()
	defer errMu.Unlock()

	if n := len(connectErrors); n > 0 {
		log.Printf("%d of %d clients failed to connect", n, totalClients)
	}
	for _, group := range []struct {
		name string
		errs []error
	}{{"connect", connectErrors}, {"enter the namespace", enterErrors}} {
		counts := make(map[string]int)
		for _, err := range group.errs {
			counts[err.Error()]++
		}
		for text, n := range counts {
			log.Printf("%s failed %d times: %s", group.name, n, text)
		}
	}

	if len(connectErrors) == 0 && len(enterErrors) == 0 {
		log.Println("all ok")
	}
}
