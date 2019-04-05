package ws

import (
	"context"

	"github.com/kataras/fastws"
)

type Client struct {
	conn *conn
}

func (c *Client) Connect(namespace string) NSConn {
	if events, ok := c.conn.namespaces[namespace]; ok {
		nsConn := newNSConn(c.conn, namespace, events)
		c.conn.addNSConn(namespace, nsConn)
		c.conn.write(Message{Namespace: namespace, isConnect: true, Event: OnNamespaceConnect})

		return nsConn
	}

	return nil
}

func (c *Client) Close() {
	c.conn.Close()
}

func Dial(ctx context.Context, url string, connHandler connHandler) (*Client, error) {
	underline, err := fastws.Dial(ctx, url)
	if err != nil {
		return nil, err
	}

	c := newConn(underline, connHandler.getNamespaces())
	go c.startWriter()
	go c.startReader()

	client := &Client{
		conn: c,
	}

	return client, nil
}
