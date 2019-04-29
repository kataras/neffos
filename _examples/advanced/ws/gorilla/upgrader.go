package gorilla

import (
	"net/http"

	"github.com/kataras/fastws/_examples/advanced/ws"

	gorilla "github.com/gorilla/websocket"
)

func Upgrader(upgrader gorilla.Upgrader) ws.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (ws.Socket, error) {
		underline, err := upgrader.Upgrade(w, r, w.Header())
		if err != nil {
			return nil, err
		}

		return newSocket(underline, false), nil
	}
}
