package gobwas

import (
	"context"

	"github.com/kataras/fastws/_examples/advanced/ws"

	gobwas "github.com/gobwas/ws"
)

var DefaultDialer = Dialer(gobwas.DefaultDialer)

func Dialer(dialer gobwas.Dialer) ws.Dialer {
	return func(ctx context.Context, url string) (ws.Socket, error) {
		underline, _, _, err := dialer.Dial(ctx, url)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
