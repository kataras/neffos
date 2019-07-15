package gobwasalive

import (
    "context"
    "github.com/RussellLuo/timingwheel"
    "time"

    "github.com/kataras/neffos"

    gobwas "github.com/gobwas/ws"
)

// DefaultDialer is a gobwas/ws dialer with all fields set to the default values.
//var DefaultDialer = Dialer(gobwas.DefaultDialer)

var twDialer = timingwheel.NewTimingWheel(50*time.Millisecond, 100)
var twDialerRunning = false

// Dialer is a `neffos.Dialer` type for the gobwas/ws subprotocol implementation.
// Should be used on `Dial` to create a new client/client-side connection.
// To send headers to the server set the dialer's `Header` field to a `gobwas.HandshakeHeaderHTTP`.
func dialer(dialer gobwas.Dialer, idleTime time.Duration) neffos.Dialer {
    return func(ctx context.Context, url string) (neffos.Socket, error) {
        underline, _, _, err := dialer.Dial(ctx, url)
        if err != nil {
            return nil, err
        }

        return newSocket(underline, nil, true, idleTime, twDialer), nil
    }
}

func NewDialer(idleTime time.Duration) neffos.Dialer {
    if !twDialerRunning {
        twDialer.Start()
        twDialerRunning = true
    }
    return dialer(gobwas.DefaultDialer, idleTime)
}
