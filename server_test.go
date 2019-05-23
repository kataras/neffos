package neffos_test

import (
	"net/http"
	"time"

	"github.com/kataras/neffos"

	gobwas "github.com/kataras/neffos/gobwas"
	gorilla "github.com/kataras/neffos/gorilla"
)

func runTestServer(addr string, connHandler neffos.ConnHandler) func() error {
	gobwasServer := neffos.New(gobwas.DefaultUpgrader, connHandler)
	gorillaServer := neffos.New(gorilla.DefaultUpgrader, connHandler)

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
