package ws

import (
	"context"

	"github.com/kataras/fastws"
)

type Client struct {
	conn *conn
}

func (c *Client) Close() {
	if c == nil || c.conn == nil {
		return
	}

	c.conn.Close()
}

// func (c *Client) Connect(namespace string, waitFromServer bool) (NSConn, error) {
// 	if waitFromServer {
// 		return c.conn.WaitConnect(namespace), nil
// 	}
// 	return c.conn.Connect(namespace)
// }

func (c *Client) WaitServerConnect(ctx context.Context, namespace string) (NSConn, error) {
	return c.conn.WaitConnect(ctx, namespace)
}

func (c *Client) Connect(ctx context.Context, namespace string) (NSConn, error) {
	return c.conn.Connect(ctx, namespace)
}

// Dial establish a new websocket client.
// Context "ctx" is used for handshake timeout.
func Dial(ctx context.Context, url string, connHandler connHandler) (*Client, error) {
	underline, err := fastws.Dial(ctx, url)
	if err != nil {
		return nil, err
	}

	readTimeout, writeTimeout := getTimeouts(connHandler)
	underline.ReadTimeout = readTimeout
	underline.WriteTimeout = writeTimeout

	c := newConn(underline, connHandler.getNamespaces())
	c.ReadTimeout = readTimeout

	go c.startReader()
	// go c.startWriter()

	client := &Client{
		conn: c,
	}

	underline.Write(ackBinary)
	// time.Sleep(1 * time.Second)
	return client, nil
}
