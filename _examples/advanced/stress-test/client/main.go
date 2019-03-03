package main

import (
	// "bufio"
	// "bytes"
	"context"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kataras/fastws/_examples/advanced/ws"
)

var (
	url      = "ws://localhost:8080"
	testdata []byte
)

const (
	totalClients = 35000 // max depends on the OS.
	verbose      = false
)

var connectionFailures uint64

var (
	disconnectErrors []error
	connectErrors    []error
	errMu            sync.Mutex
)

func collectError(op string, err error) {
	errMu.Lock()
	defer errMu.Unlock()

	switch op {
	case "disconnect":
		disconnectErrors = append(disconnectErrors, err)
	case "connect":
		connectErrors = append(connectErrors, err)
	}
}

func main() {
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

	// for i := 0; i < totalClients; i++ {
	// 	go connect(wg, time.Second-time.Duration(rand.Int63n(3))*time.Second)
	// }

	for i := 0; i < totalClients; i++ {
		if i%2 == 0 {
			time.Sleep(time.Duration(rand.Int63n(3)) * time.Millisecond)
		}

		if i%3 == 0 {
			time.Sleep(time.Duration(rand.Int63n(5)) * time.Millisecond)
		}

		go connect(wg, 1*time.Second-time.Duration(rand.Int63n(7))*time.Millisecond)
	}

	wg.Wait()

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

	if n := len(disconnectErrors); n > 0 {
		log.Printf("Finished with %d disconnect errors\n", n)
		for i, err := range disconnectErrors {
			log.Printf("[%d] - %v\n", i+1, err)
		}
	}

	if connectionFailures == 0 && len(connectErrors) == 0 && len(disconnectErrors) == 0 {
		log.Println("ALL OK.")
	}

	log.Println("-- Finished.")
}

var msg = []byte("my message")

var counter uint32

func connect(wg *sync.WaitGroup, alive time.Duration) error {
	defer wg.Done()

	// t := atomic.AddUint32(&counter, 1)

	// log.Printf("[%d] try to connect\n", t)
	c, err := ws.Dial(context.Background(), url, "")

	if err != nil {
		//	log.Printf("[%d] %s\n", t, err)
		atomic.AddUint64(&connectionFailures, 1)
		collectError("connect", err)
		return err
	}
	// if alive > 0 {
	// 	c.UnderlyingConn().NetConn.SetDeadline(time.Now().Add(alive))
	// }

	c.OnError(func(err error) {
		log.Printf("error: %v", err)
	})

	// c.(func() {
	// 	if verbose {
	// 		log.Printf("I am disconnected after [%s].", alive)
	// 	}

	// 	disconnected = true
	// })

	c.On("chat", func(message []byte) error {
		if verbose {
			log.Println(string(message))
		}
		return nil
	})

	c.Emit("chat", msg)
	// r := ioutil.NopCloser(bytes.NewBuffer(testdata))
	// scanner := bufio.NewScanner(r)
	// for scanner.Scan() {
	// 	if text := scanner.Bytes(); len(text) > 1 {
	// 		c.Emit("chat", text)
	// 		// go c.Emit("chat", "[2] "+text)
	// 	}
	// }

	//	if alive > 0 {
	//	time.Sleep(alive)
	// time.Sleep(500 * time.Millisecond)
	// c.Close()
	// if verbose {
	// 	log.Printf("I am disconnected after [%s].", alive)
	// }
	//}

	time.Sleep(alive)
	c.Close()

	// return scanner.Err()
	return nil
}
