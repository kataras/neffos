// Load test, server side: count connections and report what happened.
//
// This server pairs with ../clients, which opens -clients connections,
// sends the lines of test.data on each and closes them at staggered times.
// Every "chat" event is echoed back to its sender with NSConn.Emit (or
// relayed to everyone else with BroadcastOthers when broadcast is true).
// OnConnect and OnNamespaceDisconnect count connections; a ticker compares
// those counts with Server.GetTotalConnections and, once every client has
// come and gone, prints whether the server let go of all of them and how
// much memory it used.
//
// The backend is a command-line argument, so the same test can compare
// gorilla, gobwas and coder. Both programs must get the same -clients.
//
// The default of 100000 clients needs an operating system tuned for many
// sockets. On Windows, widen the dynamic port range first:
//
//	netsh int ipv4 set dynamicport tcp start=10000 num=36000
//	netsh int ipv6 set dynamicport tcp start=10000 num=36000
//
// On Linux, raise the open files limit (ulimit -n) and widen
// net.ipv4.ip_local_port_range.
//
// Learn: measure a neffos server under many short-lived connections.
//
// Requires: an operating system tuned for many sockets when -clients is large (see above).
//
// Run:
//
//	go run main.go -clients 1000 gobwas            # terminal 1; or gorilla, or coder
//	cd ../clients && go run main.go -clients 1000 gobwas    # terminal 2, same values
//
// Try:
//
//	# the server logs its counters every few seconds, then "all clients disconnected" and the memory stats
//
// Next: ../clients is the other half of this test.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/coder"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

const (
	addr    = ":9595"
	verbose = false
	// broadcast relays every chat line to all the other clients instead of
	// echoing it back to its sender.
	broadcast = false
)

var (
	totalClients            uint64
	started                 atomic.Bool
	totalConnected          atomic.Uint64
	totalDisconnected       atomic.Uint64
	totalNamespaceConnected atomic.Uint64
)

func main() {
	flag.Uint64Var(&totalClients, "clients", 100000, "how many clients ../clients opens")
	flag.Parse()

	upgrader := gobwas.DefaultUpgrader
	switch backend := flag.Arg(0); backend {
	case "gorilla":
		upgrader = gorilla.DefaultUpgrader
	case "coder":
		upgrader = coder.DefaultUpgrader
	case "", "gobwas":
	default:
		log.Fatalf("unknown backend %q, expected gobwas, gorilla or coder", backend)
	}
	log.Printf("expecting %d clients", totalClients)

	srv := neffos.New(upgrader, neffos.WithTimeout{
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		Events: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				totalNamespaceConnected.Add(1)
				return nil
			},
			neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
				n := totalDisconnected.Add(1)
				if verbose {
					log.Printf("[%d] client [%s] disconnected", n, c.Conn.ID())
				}
				return nil
			},
			"chat": func(c *neffos.NSConn, msg neffos.Message) error {
				if broadcast {
					c.BroadcastOthers(msg)
				} else {
					c.Emit("chat", msg.Body)
				}
				return nil
			},
		},
	})
	srv.OnConnect = func(c *neffos.Conn) error {
		totalConnected.Add(1)
		started.Store(true)
		return nil
	}
	srv.OnUpgradeError = func(err error) {
		log.Printf("upgrade error: %v", err)
	}

	go monitor(srv)

	mux := http.NewServeMux()
	mux.Handle("/ws", srv)
	log.Printf("listening on %s, websocket endpoint /ws", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// monitor logs the counters until every client has connected and gone, or
// until no connection is left for six checks in a row, then exits.
func monitor(srv *neffos.Server) {
	every := 5 * time.Second
	if totalClients >= 64000 {
		every = 10 * time.Second
	}
	ticker := time.NewTicker(every)
	defer func() {
		ticker.Stop()
		printMemUsage()
		os.Exit(0)
	}()

	idleChecks := 0
	for range ticker.C {
		n := srv.GetTotalConnections()
		connected, disconnected := totalConnected.Load(), totalDisconnected.Load()
		log.Printf("connections now: %d, connected so far: %d, disconnected: %d, entered the namespace: %d",
			n, connected, disconnected, totalNamespaceConnected.Load())

		if !started.Load() {
			continue
		}

		if connected == totalClients && disconnected == totalClients {
			if n != 0 {
				log.Printf("all clients disconnected, but %d connections are still listed", n)
			} else {
				log.Println("all clients disconnected")
			}
			return
		}

		if n == 0 {
			// Clients may still be starting, or reconnecting; give them six checks.
			if idleChecks++; idleChecks < 6 {
				continue
			}
			log.Printf("%d of %d clients never connected; check the operating system's socket limits",
				totalClients-connected, totalClients)
			return
		}
		idleChecks = 0
	}
}

func printMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("alloc = %v MiB, total alloc = %v MiB, sys = %v MiB, GC runs = %v, goroutines = %d",
		m.Alloc/1024/1024, m.TotalAlloc/1024/1024, m.Sys/1024/1024, m.NumGC, runtime.NumGoroutine())
}
