package gorilla

import (
	"net/http"

	"github.com/kataras/ws"

	gorilla "github.com/gorilla/websocket"
)

var DefaultUpgrader = Upgrader(gorilla.Upgrader{})

func Upgrader(upgrader gorilla.Upgrader) ws.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (ws.Socket, error) {
		underline, err := upgrader.Upgrade(w, r, w.Header())
		if err != nil {
			return nil, err
		}

		return newSocket(underline, r, false), nil
	}
}
