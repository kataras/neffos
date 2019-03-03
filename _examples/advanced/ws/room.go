package ws

import (
	"errors"
)

type EventListener func(Conn, []byte) error

type eventHandler struct {
	Func EventListener
}

func (h *eventHandler) fire(c Conn, body []byte) error {
	return h.Func(c, body)
}

func newEventHandler(fn EventListener) *eventHandler {
	return &eventHandler{
		Func: fn,
	}
}

type Room struct {
	OnJoin  func(Conn) error
	OnLeave func(Conn)
	OnError func(Conn, error)
	events  map[string]*eventHandler
}

func newRoom() *Room {
	return &Room{
		events: make(map[string]*eventHandler),
	}
}

func (r *Room) On(event string, fn EventListener) {
	r.events[event] = newEventHandler(fn)
}

const (
	roomJoin uint8 = iota
	roomLeave
	roomError
	roomMessage
)

type roomAction struct {
	typ uint8

	err error

	msg message
}

var errUnexpectedMessageType = errors.New("unexpected message type")

func (r *Room) handle(c *connOnRoom, action roomAction) error {
	switch action.typ {
	case roomJoin:
		if r.OnJoin != nil {
			return r.OnJoin(c)
		}
		return nil
	case roomLeave:
		if r.OnLeave != nil {
			r.OnLeave(c)
		}
		return nil
	case roomError:
		if r.OnError != nil {
			r.OnError(c, action.err)
		}
		return nil
	case roomMessage:
		h, ok := r.events[action.msg.event]
		if !ok {
			return nil
		}

		return h.fire(c, action.msg.body)
	}

	return errUnexpectedMessageType
}
