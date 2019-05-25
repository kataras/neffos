package main

import (
	"bufio"
	"bytes"
	"context"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gobwas"
	"github.com/kataras/neffos/gorilla"

	"golang.org/x/sync/semaphore"
)

var (
	url      = "ws://localhost:9595"
	testdata []byte
)

const (
	verbose = false
	// max depends on the OS.
	totalClients         = 100000
	maxConcurrentClients = 0
	// if server's `serverHandleNamespaceConnect` is true then this value should be false.
	clientHandleNamespaceConnect = true
	broadcast                    = false
)

var totalConnectedNamespace = new(uint64)

var (
	sem = semaphore.NewWeighted(maxConcurrentClients)

	handler = neffos.WithTimeout{
		ReadTimeout:  60 * time.Second, // alive,
		WriteTimeout: 60 * time.Second, // alive,
		Events: neffos.Events{
			neffos.OnNamespaceConnected: func(c *neffos.NSConn, msg neffos.Message) error {
				atomic.AddUint64(totalConnectedNamespace, 1)
				return nil
			},
			neffos.OnNamespaceDisconnect: func(c *neffos.NSConn, msg neffos.Message) error {
				if maxConcurrentClients > 0 {
					sem.Release(1)
				}

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
)

func startMonitor() func() {
	var monitorEvery = 5 * time.Second
	if totalClients >= 50000 {
		monitorEvery = 15 * time.Second
	}
	timer := time.NewTicker(monitorEvery)
	go func() {
		for {
			<-timer.C
			log.Printf("INFO: Total connected to namespace[%d]", *totalConnectedNamespace)
		}
	}()
	return timer.Stop
}

var connectionFailures uint64

var (
	disconnectErrors       []error
	connectErrors          []error
	connectNamespaceErrors []error
	errMu                  sync.Mutex
)

func collectError(op string, err error) {
	errMu.Lock()
	defer errMu.Unlock()

	switch op {
	case "disconnect":
		disconnectErrors = append(disconnectErrors, err)
	case "connect":
		connectErrors = append(connectErrors, err)
	case "connect namespace":
		connectNamespaceErrors = append(connectNamespaceErrors, err)
	}
}

func main() {
	dialer := gobwas.DefaultDialer
	if len(os.Args) > 1 {
		if os.Args[1] == "gorilla" { // go run main.go gorilla
			dialer = gorilla.DefaultDialer
			log.Printf("Using Gorilla Dialer.")
		}
	}

	log.Println("-- Running...")
	f, err := os.Open("./test.data")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	testdata, err = ioutil.ReadAll(f)
	if err != nil {
		panic(err)
	}

	start := time.Now()

	wg := new(sync.WaitGroup)
	wg.Add(totalClients)

	relaxCPU := 15 * time.Millisecond // this may not be useful if host contains high-end hardware.
	lastRelaxCPU := time.Now()
	stopMonitor := startMonitor()

	if maxConcurrentClients > 0 {
		relaxCPU = 2 * relaxCPU
	}

	var alive time.Duration
	for i := 0; i < totalClients; i++ {

		if time.Now().After(lastRelaxCPU.Add(relaxCPU)) {
			runtime.GC()
			time.Sleep(relaxCPU)
			lastRelaxCPU = time.Now()
		}

		if maxConcurrentClients > 0 {
			err := sem.Acquire(context.TODO(), 1)
			if err != nil {
				panic(err)
			}
		}

		if i%2 == 0 {
			time.Sleep(time.Duration(rand.Int63n(6)) * time.Millisecond)
			//	alive = 4*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
			alive = 4*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
		} else if i%3 == 0 {
			time.Sleep(time.Duration(rand.Int63n(6)) * time.Millisecond)
			alive = 6*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
			//	alive = 6*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
		} else {
			alive = 8*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
			//	alive = 8*time.Second - time.Duration(rand.Int63n(3))*time.Millisecond
		}

		go connect(wg, dialer, alive)
	}

	wg.Wait()
	stopMonitor()

	log.Printf("execution time [%s]", time.Since(start))
	log.Println()

	if connectionFailures > 0 {
		log.Printf("Finished with %d/%d connection failures.", connectionFailures, totalClients)
	}

	if n := len(connectErrors); n > 0 {
		log.Printf("Finished with %d connect errors: ", n)
		var lastErr error
		var sameC int

		for i, err := range connectErrors {
			if lastErr != nil {
				if lastErr.Error() == err.Error() {
					sameC++
					continue
				} else {
					_, ok := lastErr.(*net.OpError)
					if ok {
						if _, ok = err.(*net.OpError); ok {
							sameC++
							continue
						}
					}
				}
			}

			if sameC > 0 {
				log.Printf("and %d more like this...\n", sameC)
				sameC = 0
				continue
			}

			log.Printf("[%d] - %v\n", i+1, err)
			lastErr = err
		}
	}

	if n := len(connectNamespaceErrors); n > 0 {
		log.Printf("Finished with %d connect namespace errors\n", n)
		for i, err := range connectNamespaceErrors {
			log.Printf("[%d] - %v\n", i+1, err)
		}
	}

	if n := len(disconnectErrors); n > 0 {
		log.Printf("Finished with %d disconnect errors\n", n)
		for i, err := range disconnectErrors {
			log.Printf("[%d] - %v\n", i+1, err)
		}
	}

	if connectionFailures == 0 && len(connectNamespaceErrors) == 0 && len(connectErrors) == 0 && len(disconnectErrors) == 0 {
		log.Println("ALL OK.")
	}

	log.Println("-- Finished.")
}

// var msg = []byte("my message")

var counter uint32

var smallPayload = []byte("the affection")

func connect(wg *sync.WaitGroup, dialer neffos.Dialer, alive time.Duration) {
	defer wg.Done()

	// t := atomic.AddUint32(&counter, 1)

	// log.Printf("[%d] try to connect\n", t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Second))
	defer cancel()

	client, err := neffos.Dial(ctx, dialer, url, handler)

	if err != nil {
		// log.Printf("connection failure: %v\n", err)
		atomic.AddUint64(&connectionFailures, 1)
		collectError("connect", err)
		return
		// return err
	}

	if client.ID == "" {
		panic("CLIENT'S ID IS EMPTY.\n DIAL NOW SHOULD BLOCK UNTIL ID IS FILLED(ACK) AND UNTIL SERVER'S CONFIRMATION")
	}

	// defer client.Close()

	ctxConnect, cancelConnect := context.WithDeadline(context.Background(), time.Now().Add(25*time.Second))
	defer cancelConnect()

	var c *neffos.NSConn

	if clientHandleNamespaceConnect {
		c, err = client.Connect(ctxConnect, "")
	} else {
		c, err = client.WaitServerConnect(ctxConnect, "")
	}

	if err != nil {
		// atomic.AddUint64(&connectionFailures, 1)
		collectError("connect namespace", err)

		if verbose {
			log.Println(err)
		}

		return
	}

	if c.Conn.ID() == "" {
		panic("CLIENT'S CONNECTION ID IS EMPTY.\nCONNECT SHOULD BLOCK UNTIL ID IS FILLED(ACK) AND UNTIL SERVER'S CONFIRMATION TO NAMESPACE CONNECTION")
	}

	r := ioutil.NopCloser(bytes.NewBuffer(testdata))
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if text := scanner.Bytes(); len(text) > 1 {
			ok := c.Emit("chat", text)
			if !ok {
				if verbose {
					log.Printf("Client event cannot emit. Connection probably closed before starting to write actual data to the server.")
				}
				return
			}
		}
	}

	// c.Emit("chat", smallPayload)

	// no need with timeouts:
	time.Sleep(alive)
	client.Close()
}
