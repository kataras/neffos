package gobwasalive

import (
    "net/http"
    "time"

    "github.com/kataras/neffos"

    "github.com/RussellLuo/timingwheel"
    gobwas "github.com/gobwas/ws"
)

// DefaultUpgrader is a gobwas/ws HTTP Upgrader with all fields set to the default values.
//var DefaultUpgrader = Upgrader(gobwas.HTTPUpgrader{})

var twServer = timingwheel.NewTimingWheel(50*time.Millisecond, 100)
var twServerRunning = false

// Upgrader is a `neffos.Upgrader` type for the gobwas/ws subprotocol implementation.
// Should be used on `neffos.New` to construct the neffos server.
func upgrader(upgrader gobwas.HTTPUpgrader, idleTime time.Duration) neffos.Upgrader {
    return func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
        underline, _, _, err := upgrader.Upgrade(r, w)
        if err != nil {
            return nil, err
        }

        return newSocket(underline, r, false, idleTime, twServer), nil
    }
}

func NewUpgrader(idleTime time.Duration) neffos.Upgrader {
    if !twServerRunning {
        twServer.Start()
        twServerRunning = true
    }
    return upgrader(gobwas.HTTPUpgrader{}, idleTime)
}
