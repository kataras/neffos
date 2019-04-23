package main

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kataras/fastws/_examples/advanced/ws"

	"github.com/kataras/fastws"
)

const (
	endpoint  = "localhost:8080"
	broadcast = false
	verbose   = false
	maxC      = 0
	// TODO: deadline exceed is read OR write now, make it to check both read and write operations on Conn -- "IDLE".
	idleTimeout = 10 * time.Second
	// if this value is true then client's `clientHandleNamespaceConnect` should be false.
	serverHandleNamespaceConnect = false
)

var totalClients uint64 = 100000 // max depends on the OS, read more below.
// For example for windows:
//
// $ netsh int ipv4 set dynamicport tcp start=10000 num=36000
// $ netsh int ipv4 set dynamicport udp start=10000 num=36000
// $ netsh int ipv6 set dynamicport tcp start=10000 num=36000
// $ netsh int ipv6 set dynamicport udp start=10000 num=36000
//
// Optionally but good practice if you want to re-test over and over,
// close all apps and execute:
//
// $ net session /delete
//
// Note that this test is hardly depends on the host machine,
// maybe there is a case where those settings does not apply to your system.

// func init() {
// 	if broadcast {
// 		totalClients = 14000
// 	}
// }

func main() {
	srv := ws.New(ws.WithTimeout{
		ReadTimeout:  idleTimeout,
		WriteTimeout: idleTimeout,
		Events: ws.Events{
			"chat": func(c ws.NSConn, msg ws.Message) error {
				if broadcast {
					c.Server().Broadcast(c, msg)
				} else {
					c.Emit("chat", msg.Body)
				}

				return nil
			},
		},
	})

	started := false

	go func() {
		allowNZero := 0

		dur := 1500 * time.Millisecond
		if totalClients >= 64000 {
			// if more than 64000 then let's perform those checks every x seconds instead,
			// either way works.
			dur = 4 * time.Second
		}
		t := time.NewTicker(dur)
		defer func() {
			t.Stop()
			printMemUsage()
			os.Exit(0)
		}()

		//	var started bool
		for {
			<-t.C

			n := srv.GetTotalConnections()
			connectedN := atomic.LoadUint64(&totalConnected)
			disconnectedN := atomic.LoadUint64(&totalDisconnected)

			// if verbose {
			log.Printf("INFO: Current connections[%d] vs WS counter[%v] of [%d] total connected", n, connectedN-disconnectedN, connectedN)
			//	}

			// if n > 0 {
			// 	started = true
			// 	if maxC > 0 && n > maxC {
			// 		log.Printf("current connections[%d] > MaxConcurrentConnections[%d]", n, maxC)
			// 		return
			// 	}
			// }

			if started {
				if disconnectedN == totalClients && connectedN == totalClients {
					if n != 0 {
						log.Printf("ALL CLIENTS DISCONNECTED BUT %d LEFTOVERS ON CONNECTIONS LIST.", n)
					} else {
						log.Println("ALL CLIENTS DISCONNECTED SUCCESSFULLY.")
					}
					return
				} else if n == 0 {
					if allowNZero < 15 {
						// Allow 0 active connections just ten times.
						// It is actually a dynamic timeout of 15*the expected total connections variable.
						// It exists for two reasons:
						// 1: user delays to start client,
						// 2: live connections may be disconnected so we are waiting for new one (randomly)
						allowNZero++
						continue
					}
					log.Printf("%v/%d CLIENTS WERE NOT CONNECTED AT ALL. CHECK YOUR OS NET SETTINGS. THE REST CLIENTS WERE DISCONNECTED SUCCESSFULLY.\n",
						totalClients-totalConnected, totalClients)

					return
				}
				allowNZero = 0
			}
		}
	}()

	srv.OnConnect = func(c ws.Conn) error {
		started = true
		atomic.AddUint64(&totalConnected, 1)
		if serverHandleNamespaceConnect {
			_, err := c.Connect("")
			return err
		}

		return nil
	}

	srv.OnError = func(c ws.Conn, err error) bool {
		log.Printf("ERROR: [%s] %v\n", c.ID(), err)
		return true
	}

	// if c.Err() != nil {
	// 	log.Fatalf("[%d] upgrade failed: %v", atomic.LoadUint64(&totalConnected)+1, c.Err())
	// 	return
	// }

	//	srv.OnError("", func(c ws.Conn, err error) { handleErr(c, err) })
	srv.OnDisconnect = handleDisconnect

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	log.Fatal(http.ListenAndServe(endpoint, srv))
}

var (
	totalConnected    uint64
	totalDisconnected uint64
)

func handleDisconnect(c ws.Conn) {
	newC := atomic.AddUint64(&totalDisconnected, 1)
	if verbose {
		log.Printf("[%d] client [%s] disconnected!\n", newC, c.ID())
	}
}

func handleErr(c ws.Conn, err error) {
	if !fastws.IsDisconnected(err) {
		log.Printf("client [%s] errorred: %v\n", c.ID(), err)
	}

}

func toMB(b uint64) uint64 {
	return b / 1024 / 1024
}

func printMemUsage() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("Alloc = %v MiB", toMB(m.Alloc))
	log.Printf("\tTotalAlloc = %v MiB", toMB(m.TotalAlloc))
	log.Printf("\tSys = %v MiB", toMB(m.Sys))
	log.Printf("\tNumGC = %v\n", m.NumGC)
	log.Printf("\tNumGoRoutines = %d\n", runtime.NumGoroutine())
}
