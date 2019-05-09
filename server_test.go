package ws_test

import (
	"net/http"
	"time"

	"github.com/kataras/ws"

	gobwas "github.com/kataras/ws/gobwas"
	gorilla "github.com/kataras/ws/gorilla"
)

func runTestServer(addr string, connHandler ws.ConnHandler) func() error {
	gobwasServer := ws.New(gobwas.DefaultUpgrader, connHandler)
	gorillaServer := ws.New(gorilla.DefaultUpgrader, connHandler)

	mux := http.NewServeMux()
	mux.Handle("/gobwas", gobwasServer)
	mux.Handle("/gorilla", gorillaServer)

	httpServer := http.Server{
		Addr:    addr,
		Handler: mux,
	}
	go httpServer.ListenAndServe()
	time.Sleep(200 * time.Millisecond)

	// teardown.
	return func() error {
		gorillaServer.Close()
		gobwasServer.Close()
		return httpServer.Close()
	}
}
