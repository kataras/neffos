package gorilla

import (
	"context"
	"net/http"

	"github.com/kataras/fastws/_examples/advanced/ws"

	gorilla "github.com/gorilla/websocket"
)

var DefaultDialer = Dialer(gorilla.DefaultDialer, make(http.Header))

func Dialer(dialer *gorilla.Dialer, requestHeader http.Header) ws.Dialer {
	return func(ctx context.Context, url string) (ws.Socket, error) {
		underline, _, err := dialer.DialContext(ctx, url, requestHeader)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
