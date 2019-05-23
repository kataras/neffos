package gorilla

import (
	"net/http"

	"github.com/kataras/neffos"

	gorilla "github.com/gorilla/websocket"
)

var DefaultUpgrader = Upgrader(gorilla.Upgrader{})

func Upgrader(upgrader gorilla.Upgrader) neffos.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
		underline, err := upgrader.Upgrade(w, r, w.Header())
		if err != nil {
			return nil, err
		}

		return newSocket(underline, r, false), nil
	}
}
