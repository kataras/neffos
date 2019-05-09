package gobwas

import (
	"net/http"

	"github.com/kataras/ws"

	gobwas "github.com/gobwas/ws"
)

var DefaultUpgrader = Upgrader(gobwas.HTTPUpgrader{})

func Upgrader(upgrader gobwas.HTTPUpgrader) ws.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (ws.Socket, error) {
		underline, _, _, err := upgrader.Upgrade(r, w)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, r, false), nil
	}
}
