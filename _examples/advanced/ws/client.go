package ws

import (
	"context"

	"github.com/kataras/fastws"
)

type Client struct {
	conn *conn
}

func (c *Client) Close() {
	c.conn.Close()
}

func (c *Client) Connect(namespace string) (NSConn, error) {
	return c.conn.Connect(namespace)
}

func Dial(ctx context.Context, url string, connHandler connHandler) (*Client, error) {
	underline, err := fastws.Dial(ctx, url)
	if err != nil {
		return nil, err
	}

	c := newConn(underline, connHandler.getNamespaces())
	err = c.ack()
	if err != nil {
		return nil, err
	}

	go c.startWriter()
	go c.startReader()

	client := &Client{
		conn: c,
	}

	return client, nil
}
