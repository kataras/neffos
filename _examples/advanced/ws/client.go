package ws

import (
	"context"

	"github.com/kataras/fastws"
)

type Client struct {
	Conn
	room *Room
}

func (c *Client) OnError(cb func(error)) {
	c.room.OnError = func(conn Conn, err error) {
		cb(err)
	}
}

func (c *Client) On(event string, cb func([]byte) error) {
	c.room.On(event, func(conn Conn, data []byte) error {
		return cb(data)
	})
}

func Dial(ctx context.Context, url, ns string) (*Client, error) {
	underline, err := fastws.Dial(ctx, url+"?ns="+ns)
	if err != nil {
		return nil, err
	}

	room := newRoom()
	conn := newConn(underline, map[string]*Room{
		ns: room,
	})

	c := conn.wrapConnRoom(ns)
	go c.startWriter()
	go c.startReader()

	client := &Client{
		Conn: c,
		room: room,
	}

	return client, nil
}
