package neffos

import (
	"time"
)

// ConnHandler is the interface which
// `Events`, `Namespaces` and `WithTimeout` are implement.
// It's exported just to be used on the `Dial`(client) and `New` (server) functions.
type ConnHandler interface {
	getNamespaces() Namespaces
}

var (
	_ ConnHandler = (Events)(nil)
	_ ConnHandler = (Namespaces)(nil)
	_ ConnHandler = WithTimeout{}
)

// Events completes the `ConnHandler` interface.
// It is a map which its key is the event name
// and its value the event's callback.
//
// Events type completes the `ConnHandler` itself therefore,
// can be used as standalone value on the `New` and `Dial` functions
// to register events on empty namespace as well.
//
// See `Namespaces`, `New` and `Dial` too.
type Events map[string]MessageHandlerFunc

func (e Events) getNamespaces() Namespaces {
	return Namespaces{"": e}
}

func (e Events) fireEvent(c *NSConn, msg Message) error {
	if h, ok := e[msg.Event]; ok {
		return h(c, msg)
	}

	if h, ok := e[OnAnyEvent]; ok {
		return h(c, msg)
	}

	return nil
}

// Namespaces completes the `ConnHandler` interface.
// Can be used to register one or more namespaces on the `New` and `Dial` functions.
// The key is the namespace literal and the value is the `Events`,
// a map with event names and their callbacks.
//
// See `WithTimeout`, `New` and `Dial` too.
type Namespaces map[string]Events

func (nss Namespaces) getNamespaces() Namespaces { return nss }

// WithTimeout completes the `ConnHandler` interface.
// Can be used to register namespaces and events or just events on an empty namespace
// with Read and Write timeouts.
//
// See `New` and `Dial`.
type WithTimeout struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	Namespaces Namespaces
	Events     Events
}

func (t WithTimeout) getNamespaces() Namespaces {
	return JoinConnHandlers(t.Namespaces, t.Events).getNamespaces()
}

func getTimeouts(h ConnHandler) (readTimeout time.Duration, writeTimeout time.Duration) {
	if t, ok := h.(WithTimeout); ok {
		readTimeout = t.ReadTimeout
		writeTimeout = t.WriteTimeout
	}

	return
}

// JoinConnHandlers combines two or more "connHandlers"
// and returns a result of a single `ConnHandler` that
// can be passed on the `New` and `Dial` functions.
func JoinConnHandlers(connHandlers ...ConnHandler) ConnHandler {
	namespaces := Namespaces{}

	for _, h := range connHandlers {
		nss := h.getNamespaces()
		if len(nss) > 0 {
			for namespace, events := range nss {
				if events == nil {
					continue
				}
				clonedEvents := make(Events, len(events))
				for evt, cb := range events {
					clonedEvents[evt] = cb
				}

				if curEvents, exists := namespaces[namespace]; exists {
					// fill missing events.
					for evt, cb := range clonedEvents {
						curEvents[evt] = cb
					}

				} else {
					namespaces[namespace] = clonedEvents
				}

			}
		}
	}

	return namespaces
}
