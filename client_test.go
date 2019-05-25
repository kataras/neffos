package neffos_test

import (
	"fmt"

	"github.com/kataras/neffos"

	gobwas "github.com/kataras/neffos/gobwas"
	gorilla "github.com/kataras/neffos/gorilla"
)

func runTestClient(addr string, connHandler neffos.ConnHandler, testFn func(string, *neffos.Client)) error {
	gobwasClient, err := neffos.Dial(nil, gobwas.DefaultDialer, fmt.Sprintf("ws://%s/gobwas", addr), connHandler)
	if err != nil {
		return err
	}
	gorillaClient, err := neffos.Dial(nil, gorilla.DefaultDialer, fmt.Sprintf("ws://%s/gorilla", addr), connHandler)
	if err != nil {
		return err
	}

	// teardown.
	teardown := func() error {
		gobwasClient.Close()
		gorillaClient.Close()
		return nil
	}
	defer teardown()

	testFn("gobwas", gobwasClient)
	testFn("gorilla", gorillaClient)
	return nil
}
