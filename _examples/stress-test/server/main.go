package main

import (
	"log"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"
)

const (
	endpoint = "localhost:9595"
	verbose  = false
	// max depends on the OS, read more below.
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
	totalClients uint64 = 100000
	// if this value is true then client's `clientHandleNamespaceConnect` should be false.
	serverHandleNamespaceConnect = false
	broadcast                    = false
)

var (
	started                 bool
	totalNamespaceConnected = new(uint64)
)

func main() {
	upgrader := gobwas.DefaultUpgrader
	if len(os.Args) > 1 {
		if os.Args[1] == "gorilla" { // go run main.go gorilla
			upgrader = gorilla.DefaultUpgrader
			log.Printf("Using Gorilla Upgrader.")
		}
	}

	srv := neffos.New(upgrader, neffos.WithTimeout{
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 60 * time.Second,
		Events: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				if msg.Err != nil {
					//	if verbose {
					log.Println(msg.Err)
					//	}
				}
				atomic.AddUint64(totalNamespaceConnected, 1)
				return nil
			},
			neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
				// if !c.isAcknowledged() {
				// 	log.Printf("[%s] on namespace[%s] disconnecting without even acknowledged first.", c.ID(), msg.Namespace)
				// }

				newC := atomic.AddUint64(&totalDisconnected, 1)
				if verbose {
					log.Printf("[%d] client [%s] disconnected!\n", newC, c.Conn.ID())
				}

				return nil
			},
			"chat": func(c *neffos.NSConn, msg neffos.Message) error {
				if broadcast {
					c.Conn.Server().Broadcast(c.Conn, msg)
				} else {
					c.Emit("chat", msg.Body)
				}

				return nil
			},
		},
	})

	go func() {
		allowNZero := 0

		dur := 5 * time.Second
		if totalClients >= 64000 {
			// if more than 64000 then let's perform those checks every x seconds instead,
			// either way works.
			dur = 10 * time.Second
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
			log.Printf("INFO: Current connections[%d] vs test counter[%v] of [%d] total connected", n, connectedN-disconnectedN, connectedN)
			log.Printf("INFO: Total connected to namespace[%d]", atomic.LoadUint64(totalNamespaceConnected))
			//	}

			// if n > 0 {
			// 	started = true
			// 	if maxC > 0 && n > maxC {
			// 		log.Printf("current connections[%d] > MaxConcurrentConnections[%d]", n, maxC)
			// 		return
			// 	}
			// }

			if started {
				if disconnectedN == totalClients && connectedN == totalClients && *totalNamespaceConnected == totalClients {
					if n != 0 {
						log.Printf("ALL CLIENTS DISCONNECTED BUT %d LEFTOVERS ON CONNECTIONS LIST.", n)
					} else {
						log.Println("ALL CLIENTS DISCONNECTED SUCCESSFULLY.")
					}
					return
				} else if n == 0 /* && *totalNamespaceConnected == totalClients */ {
					if allowNZero < 6 {
						// Allow 0 active connections just ten times.
						// It is actually a dynamic timeout of 6*the expected total connections variable.
						// It exists for two reasons:
						// 1: user delays to start client,
						// 2: live connections may be disconnected so we are waiting for new one (randomly)
						allowNZero++
						continue
					}

					if n != connectedN {
						log.Printf("%d CLIENT(S) FAILED TO CONNECT TO THE NAMESPACE", 1+(connectedN-disconnectedN)-n)
					} else if totalClients-totalConnected > 0 {
						log.Printf("%v/%d CLIENT(S) WERE NOT CONNECTED AT ALL. CHECK YOUR OS NET SETTINGS. THE REST CLIENTS WERE DISCONNECTED SUCCESSFULLY.\n",
							totalClients-totalConnected, totalClients)
					}
					return
				}
				allowNZero = 0
			}
		}
	}()

	srv.OnConnect = func(c *neffos.Conn) error {
		n := atomic.AddUint64(&totalConnected, 1)
		if n == 1 {
			started = true
		}

		if serverHandleNamespaceConnect {
			_, err := c.Connect(nil, "")
			return err
		}

		return nil
	}

	srv.OnUpgradeError = func(err error) {
		log.Printf("Upgrade error: %v\n", err)
	}

	// if c.Err() != nil {
	// 	log.Fatalf("[%d] upgrade failed: %v", atomic.LoadUint64(&totalConnected)+1, c.Err())
	// 	return
	// }

	//	srv.OnError("", func(c *neffos.Conn, err error) { handleErr(c, err) })
	//	srv.OnDisconnect = handleDisconnect

	log.Printf("Listening on: %s\nPress CTRL/CMD+C to interrupt.", endpoint)
	log.Fatal(http.ListenAndServe(endpoint, srv))
}

var (
	totalConnected    uint64
	totalDisconnected uint64
)

func handleDisconnect(c *neffos.Conn) {
	newC := atomic.AddUint64(&totalDisconnected, 1)
	if verbose {
		log.Printf("[%d] client [%s] disconnected!\n", newC, c.ID())
	}
}

func handleErr(c *neffos.Conn, err error) {
	if !neffos.IsDisconnectError(err) {
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
