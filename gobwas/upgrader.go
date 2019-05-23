package gobwas

import (
	"net/http"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
)

var DefaultUpgrader = Upgrader(gobwas.HTTPUpgrader{})

func Upgrader(upgrader gobwas.HTTPUpgrader) neffos.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
		underline, _, _, err := upgrader.Upgrade(r, w)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, r, false), nil
	}
}
