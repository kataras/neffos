package gorilla

import (
	"context"
	"net/http"

	"github.com/WolffunGame/wolfsocket"

	gorilla "github.com/gorilla/websocket"
)

// DefaultDialer is a gorilla/websocket dialer with all fields set to the default values.
var DefaultDialer = Dialer(gorilla.DefaultDialer, make(http.Header))

// Dialer is a `wolfsocket.Dialer` type for the gorilla/websocket subprotocol implementation.
// Should be used on `Dial` to create a new client/client-side connection.
func Dialer(dialer *gorilla.Dialer, requestHeader http.Header) wolfsocket.Dialer {
	return func(ctx context.Context, url string) (wolfsocket.Socket, error) {
		underline, _, err := dialer.DialContext(ctx, url, requestHeader)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
