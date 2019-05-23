package gorilla

import (
	"context"
	"net/http"

	"github.com/kataras/neffos"

	gorilla "github.com/gorilla/websocket"
)

var DefaultDialer = Dialer(gorilla.DefaultDialer, make(http.Header))

func Dialer(dialer *gorilla.Dialer, requestHeader http.Header) neffos.Dialer {
	return func(ctx context.Context, url string) (neffos.Socket, error) {
		underline, _, err := dialer.DialContext(ctx, url, requestHeader)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
