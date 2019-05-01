package ws

import (
	"time"
)

type connHandler interface {
	getNamespaces() Namespaces
}

var (
	_ connHandler = (Events)(nil)
	_ connHandler = (Namespaces)(nil)
)

var (
	OnNamespaceConnect    = "_OnNamespaceConnect"
	OnNamespaceConnected  = "_OnNamespaceConnected"
	OnNamespaceDisconnect = "_OnNamespaceDisconnect" // if allowed to connect then it's allowed to disconnect as well.
	NoOpEvent             = "_NoOp"
)

type Events map[string]MessageHandlerFunc

func (e Events) getNamespaces() Namespaces {
	return Namespaces{"": e}
}

func (e Events) fireOnNamespaceConnect(c NSConn, msg Message) error {
	if h, ok := e[OnNamespaceConnect]; ok {
		return h(c, msg)
	}

	return nil
}

func (e Events) fireOnNamespaceDisconnect(c NSConn, msg Message) error {
	if h, ok := e[OnNamespaceDisconnect]; ok {
		return h(c, msg)
	}

	return nil
}

func (e Events) fireEvent(c NSConn, msg Message) error {
	if h, ok := e[msg.Event]; ok {
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

func getTimeouts(h connHandler) (readTimeout time.Duration, writeTimeout time.Duration) {
	if t, ok := h.(WithTimeout); ok {
		readTimeout = t.ReadTimeout
		writeTimeout = t.WriteTimeout
	}

	return
}

func joinConnHandlers(connHandlers ...connHandler) connHandler {
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
