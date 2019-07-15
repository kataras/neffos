package neffos

import (
	"fmt"
)

func ExampleEvents_On() {
	events := make(Events)
	events.On(OnNamespaceConnected, nil)
	events.On("chat", nil)

	fmt.Println(len(events)) // we can't loop them and expect the same order ofc.
	// Output:
	// 2
}

func ExampleNamespaces_On() {
	nss := make(Namespaces)
	nss.On("default", OnNamespaceConnected, nil).
		On("chat", nil) // registers on "default"

	nss.On("other", "chat", nil)
	nss.On("other", "event", nil)

	fmt.Println(len(nss))
	// Output:
	// 2
}

type testExampleConnHandler struct{}

func (h *testExampleConnHandler) OnConnected(*NSConn, Message) error { return nil }

func (h *testExampleConnHandler) OnChat(*NSConn, Message) error { return nil }

func (h *testExampleConnHandler) GetNamespaces() Namespaces {
	nss := make(Namespaces)
	nss.On("default", OnNamespaceConnected, h.OnConnected).
		On("chat", h.OnChat) // registers on "default"
	nss.On("other", "chat", h.OnChat)
	nss.On("other2", "chat", h.OnChat)

	return nss
}

var _ ConnHandler = (*testExampleConnHandler)(nil)

func ExampleConnHandler_GetNamespaces() {
	h := new(testExampleConnHandler)

	fmt.Println(len(h.GetNamespaces()))
	// Output:
	// 3
}
