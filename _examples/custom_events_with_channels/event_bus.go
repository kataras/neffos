package main

import (
	"fmt"

	"github.com/kataras/fastws"
)

// The "defaultEvent" is one more default event that all connections are subscribed automatically too,
// so they can share messages.
const defaultEvent = "public"

type actionType uint32

const (
	subscribeAction actionType = iota
	unsubscribeAction
	unsubscribeAllAction
)

type action struct {
	conn  *fastws.Conn
	event string
	typ   actionType
}

type publishMessage struct {
	From  string `json:"from"`  // the connection id.
	Event string `json:"event"` // the event to publish to.
	Text  string `json:"text"`  // the message text.
}

func (msg publishMessage) String() string {
	return fmt.Sprintf("<%s> %s", msg.From, msg.Text)
}

type eventBus struct {
	events map[string][]*fastws.Conn

	broadcastCh chan publishMessage
	actionCh    chan action

	errorCh chan error
}

func (b *eventBus) start() {
	for {
		select {
		case msg := <-b.broadcastCh:
			conns := b.events[msg.Event]
			for _, conn := range conns {
				if conn.ID == msg.From { // except "myself".
					continue
				}

				err := conn.Encode(msg)
				if err != nil {
					go func() {
						b.errorCh <- err
					}()
				}
			}
		case act := <-b.actionCh:
			if act.typ == unsubscribeAllAction {
				for evt, conns := range b.events {
					for i, conn := range conns {
						if conn.ID == act.conn.ID {
							// println("delete " + c.ID + " from " + evt)
							b.events[evt] = append(conns[:i], conns[i+1:]...)
							break // break "conns".
						}
					}
				}
				continue
			}

			conns := b.events[act.event]

			subscribed := false
			for i, conn := range conns { // always non-nil because it defaults to itself.
				if conn.ID == act.conn.ID {
					if act.typ == unsubscribeAction {
						b.events[act.event] = append(conns[:i], conns[i+1:]...)
					} else {
						// already subscribed.
						subscribed = true
					}
					break
				}
			}

			if act.typ == subscribeAction && !subscribed {
				b.events[act.event] = append(conns, act.conn)
			}
		}

	}
}

func (b *eventBus) broadcast(msg publishMessage) {
	b.broadcastCh <- msg
}

func (b *eventBus) subscribe(c *fastws.Conn, event string) {
	b.actionCh <- action{conn: c, event: event, typ: subscribeAction}
}

func (b *eventBus) unsubscribe(c *fastws.Conn, event string) {
	b.actionCh <- action{conn: c, event: event, typ: unsubscribeAction}
}

func (b *eventBus) put(c *fastws.Conn) {
	b.subscribe(c, c.ID)
}

func (b *eventBus) del(c *fastws.Conn) {
	b.actionCh <- action{conn: c, typ: unsubscribeAllAction}
}
