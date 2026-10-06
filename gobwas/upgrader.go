package gobwas

import (
	"io"
	"net/http"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
)

// DefaultUpgrader is a gobwas/ws HTTP Upgrader with all fields set to the default values.
var DefaultUpgrader = Upgrader(gobwas.HTTPUpgrader{})

// Upgrader is a `neffos.Upgrader` type for the gobwas/ws subprotocol implementation.
// Should be used on `neffos.New` to construct the neffos server.
func Upgrader(upgrader gobwas.HTTPUpgrader) neffos.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
		underline, rw, _, err := upgrader.Upgrade(r, w)
		if err != nil {
			return nil, err
		}

		s := newSocket(underline, r, false)

		// net/http may have read past the handshake already, so the first
		// frames a client sent right behind its request can sit in this
		// buffer. Read them before going back to the connection.
		if rw != nil {
			if n := rw.Reader.Buffered(); n > 0 {
				s.reader.Source = io.MultiReader(io.LimitReader(rw.Reader, int64(n)), underline)
			}
		}

		return s, nil
	}
}
