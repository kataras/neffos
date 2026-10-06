package neffos_test

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/kataras/neffos"
)

func TestJoinAndLeaveRoom(t *testing.T) {
	var (
		wg        sync.WaitGroup
		namespace = "default"
		roomName  = "room1"
		body      = []byte("data")
		events    = neffos.Namespaces{
			namespace: neffos.Events{
				// The IsClient() branch runs on the connection's own
				// goroutine, so it must use t.Error, not t.Fatal.
				"event": func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						defer wg.Done()

						if !bytes.Equal(msg.Body, body) {
							t.Errorf("expected event's incoming data to be: %s but got: %s", string(body), string(msg.Body))
							return nil
						}

						room := c.Room(roomName)
						if room == nil {
							t.Error("expected a non-nil room")
							return nil
						}

						if room.Name != msg.Room {
							t.Errorf("expected Message's room name to be: %s==%s but it's: %s", roomName, room.Name, msg.Room)
							return nil
						}

						room.Leave(context.TODO())
					} else {
						c.Conn.Server().Broadcast(nil, msg)
					}

					return nil
				},
				neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						defer wg.Done()

						if msg.Room != roomName {
							t.Errorf("expected left room name to be %s but got %s", roomName, msg.Room)
						}
					}

					return nil
				},
			},
		}
	)

	ts := newTestServers(t, events)

	ts.dial(t, events,
		func(backend string, client *neffos.Client) {
			c, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}

			room, err := c.JoinRoom(context.TODO(), roomName)
			if err != nil {
				t.Fatal(err)
			}

			if room == nil {
				t.Fatal("expected a non-nil room")
			}

			if room.Name != roomName {
				t.Fatalf("expected joined room name to be: %s but got: %s", roomName, room.Name)
			}

			// 1 -> to catch its own event and
			// 2 -> to notify about room leave inside the event itself for both clients ofc.
			wg.Add(2)
			ok := room.Emit("event", body)
			if !ok {
				t.Fatalf("expected true")
			}
			wg.Wait()
		})
}

func TestJoinAndLeaveRoomInsideHandler(t *testing.T) {
	var (
		wg        sync.WaitGroup
		namespace = "default"
		roomName  = "room1"
		body      = []byte("data")
		events    = neffos.Namespaces{
			namespace: neffos.Events{
				// The IsClient() branch runs on the connection's own
				// goroutine, so it must use t.Error, not t.Fatal.
				"event": func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						defer wg.Done()

						if !bytes.Equal(msg.Body, body) {
							t.Errorf("expected event's incoming data to be: %s but got: %s", string(body), string(msg.Body))
							return nil
						}

						room := c.Room(roomName)
						if room == nil {
							t.Error("expected a non-nil room")
							return nil
						}

						if room.Name != msg.Room {
							t.Errorf("expected Message's room name to be: %s==%s but it's: %s", roomName, room.Name, msg.Room)
							return nil
						}

						room.Leave(context.TODO())
					} else {
						room, err := c.JoinRoom(context.TODO(), roomName)
						if err != nil {
							return err
						}

						room.Emit(msg.Event, msg.Body)
					}

					return nil
				},
				neffos.OnRoomLeft: func(c *neffos.NSConn, msg neffos.Message) error {
					if c.Conn.IsClient() {
						defer wg.Done()

						if msg.Room != roomName {
							t.Errorf("expected left room name to be %s but got %s", roomName, msg.Room)
						}
					}

					return nil
				},
			},
		}
	)

	ts := newTestServers(t, events)

	ts.dial(t, events,
		func(backend string, client *neffos.Client) {
			c, err := client.Connect(context.TODO(), namespace)
			if err != nil {
				t.Fatal(err)
			}

			// 1 -> to catch its own event and
			// 2 -> to notify about room leave inside the event itself for both clients ofc.
			wg.Add(2)
			c.Emit("event", body)
			wg.Wait()
		})
}
