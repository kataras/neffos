package ws

import (
	"time"
)

type ConnHandler interface {
	getNamespaces() Namespaces
}

var (
	_ ConnHandler = (Events)(nil)
	_ ConnHandler = (Namespaces)(nil)
	_ ConnHandler = WithTimeout{}
)

var (
	OnNamespaceConnect    = "_OnNamespaceConnect"
	OnNamespaceConnected  = "_OnNamespaceConnected"
	OnNamespaceDisconnect = "_OnNamespaceDisconnect" // if allowed to connect then it's allowed to disconnect as well.
	OnRoomJoin            = "_OnRoomJoin"
	OnRoomLeave           = "_OnRoomLeave"
	OnAnyEvent            = "_OnAnyEvent" // when event no match.
)

func IsSystemEvent(event string) bool {
	switch event {
	case OnNamespaceConnect, OnNamespaceConnected, OnNamespaceDisconnect, OnRoomJoin, OnRoomLeave:
		return true
	default:
		return false
	}
}

type Events map[string]MessageHandlerFunc

func (e Events) getNamespaces() Namespaces {
	return Namespaces{"": e}
}

func (e Events) fireEvent(c NSConn, msg Message) error {
	if h, ok := e[msg.Event]; ok {
		return h(c, msg)
	}

	if h, ok := e[OnAnyEvent]; ok {
		return h(c, msg)
	}

	return nil
}

type Namespaces map[string]Events

func (nss Namespaces) getNamespaces() Namespaces { return nss }

type WithTimeout struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	Namespaces Namespaces
	Events     Events
}

func (t WithTimeout) getNamespaces() Namespaces {
	return joinConnHandlers(t.Namespaces, t.Events).getNamespaces()
}

func getTimeouts(h ConnHandler) (readTimeout time.Duration, writeTimeout time.Duration) {
	if t, ok := h.(WithTimeout); ok {
		readTimeout = t.ReadTimeout
		writeTimeout = t.WriteTimeout
	}

	return
}

func joinConnHandlers(connHandlers ...ConnHandler) ConnHandler {
	namespaces := Namespaces{}

	for _, h := range connHandlers {
		nss := h.getNamespaces()
		if len(nss) > 0 {
			for namespace, events := range nss {
				namespaces[namespace] = events
			}
		}
	}

	return namespaces
}
