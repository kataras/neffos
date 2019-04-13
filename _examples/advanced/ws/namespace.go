package ws

import (
	"net/http"
)

type NSAcceptor func(r *http.Request, namespace string) bool

var DefaultNSAcceptor = func(r *http.Request, namespace string) bool {
	return r.URL.Query().Get("ns") == namespace
}

// type MessageHandler interface {
// 	HandleMessage(c Conn, msg Message) error
// }

type MessageHandlerFunc func(NSConn, Message) error

type reply struct {
	Body []byte
}

func (r reply) Error() string {
	return ""
}

func isReply(err error) ([]byte, bool) {
	if err != nil {
		if r, ok := err.(reply); ok {
			return r.Body, true
		}
	}
	return nil, false
}

func Reply(body []byte) error {
	return reply{body}
}

// func (h MessageHandlerFunc) HandleMessage(c Conn, msg Message) error {
// 	return h(c, msg)
// }

// type NS struct {
// 	name   string                   // name is a unique identifier, read-only on serve-time.
// 	events map[string]*EventHandler // read-only on serve-time, it is always non-nil.

// 	Acceptor     NSAcceptor // if nil then it binds to the Server.AcceptNamespace.
// 	OnConnect    func(Conn)
// 	OnDisconnect func(Conn)
// 	OnError      func(Conn, error)

// 	autoCreated bool // when created automatically, dynamically from Server.Namespace.
// 	isClone     bool // when this namespace is a result of NS.Clone
// }

// func NewNamespace(name string) *NS {
// 	ns := &NS{
// 		name:   name,
// 		events: make(map[string]*EventHandler),
// 	}

// 	return ns
// }

// func (ns *NS) String() string {
// 	return ns.name
// }

// type EventHandler struct {
// 	Event   string
// 	Handler MessageHandler
// }

// func (ns *NS) Handle(event string, handler MessageHandler) {
// 	ns.events[event] = &EventHandler{
// 		Event:   event,
// 		Handler: handler,
// 	}
// }

// func (ns *NS) HandleFunc(event string, handlerFunc func(Conn, Message) error) {
// 	ns.Handle(event, MessageHandlerFunc(handlerFunc))
// }

// func (ns *NS) fireOnConnect(c Conn) {
// 	if ns.OnConnect != nil {
// 		ns.OnConnect(c)
// 	}
// }

// func (ns *NS) fireOnDisconnect(c Conn) {
// 	if ns.OnDisconnect != nil {
// 		ns.OnDisconnect(c)
// 	}
// }

// func (ns *NS) fireOnError(c Conn, err error) {
// 	if ns.OnError != nil {
// 		ns.OnError(c, err)
// 	}
// }

// func (ns *NS) fireEvent(c Conn, msg Message) error {
// 	h := ns.events[msg.Event]
// 	if h == nil {
// 		return nil
// 	}

// 	return h.Handler.HandleMessage(c, msg)
// }

// func (ns *NS) Accept(r *http.Request) bool {
// 	if ns.Acceptor == nil {
// 		return DefaultNSAcceptor(r, ns)
// 	}

// 	return ns.Acceptor(r, ns)
// }

// func (ns *NS) IsAutoCreated() bool {
// 	return ns.autoCreated
// }

// func (ns *NS) IsClone() bool {
// 	return ns.isClone
// }

// func (ns *NS) Clone(cloneName string) *NS {
// 	newNS := new(NS)
// 	newNS.isClone = true
// 	newNS.autoCreated = false
// 	newNS.name = cloneName

// 	if len(ns.events) > 0 {
// 		newNS.events = make(map[string]*EventHandler, len(ns.events))
// 		for _, evt := range ns.events {
// 			newNS.events[evt.Event] = &EventHandler{
// 				Event:   evt.Event,
// 				Handler: evt.Handler,
// 			}
// 		}
// 	} else {
// 		ns.events = make(map[string]*EventHandler)
// 	}

// 	newNS.Acceptor = ns.Acceptor
// 	newNS.OnConnect = ns.OnConnect
// 	newNS.OnDisconnect = ns.OnDisconnect
// 	newNS.OnError = ns.OnError
// 	return newNS
// }
