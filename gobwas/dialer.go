package gobwas

import (
	"context"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
)

var DefaultDialer = Dialer(gobwas.DefaultDialer)

func Dialer(dialer gobwas.Dialer) neffos.Dialer {
	return func(ctx context.Context, url string) (neffos.Socket, error) {
		underline, _, _, err := dialer.Dial(ctx, url)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
