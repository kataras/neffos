package ws_test

import (
	"fmt"

	"github.com/kataras/fastws/_examples/advanced/ws"

	gobwas "github.com/kataras/fastws/_examples/advanced/ws/gobwas"
	gorilla "github.com/kataras/fastws/_examples/advanced/ws/gorilla"
)

func runTestClient(addr string, connHandler ws.ConnHandler, testFn func(string, *ws.Client)) error {
	gobwasClient, err := ws.Dial(gobwas.DefaultDialer, nil, fmt.Sprintf("ws://%s/gobwas", addr), connHandler)
	if err != nil {
		return err
	}
	gorillaClient, err := ws.Dial(gorilla.DefaultDialer, nil, fmt.Sprintf("ws://%s/gorilla", addr), connHandler)
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
