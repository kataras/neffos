package gobwas

import (
	"bufio"
	"context"
	"io"

	"github.com/kataras/neffos"

	gobwas "github.com/gobwas/ws"
)

// DefaultDialer is a gobwas/ws dialer with all fields set to the default values.
var DefaultDialer = Dialer(gobwas.DefaultDialer)

// Dialer is a `neffos.Dialer` type for the gobwas/ws subprotocol implementation.
// Should be used on `Dial` to create a new client/client-side connection.
// To send headers to the server set the dialer's `Header` field to a `gobwas.HandshakeHeaderHTTP`.
func Dialer(dialer gobwas.Dialer) neffos.Dialer {
	return func(ctx context.Context, url string) (neffos.Socket, error) {
		underline, br, _, err := dialer.Dial(ctx, url)
		if err != nil {
			return nil, err
		}

		s := newSocket(underline, nil, true)

		// gobwas returns br only when the server sent frames right behind
		// its handshake response and they were read with it. Read those
		// first, then hand br back to gobwas's pool and go on with the
		// connection.
		if br != nil {
			buffered := &pooledReader{r: io.LimitReader(br, int64(br.Buffered())), br: br}
			s.reader.Source = io.MultiReader(buffered, underline)
		}

		return s, nil
	}
}

// pooledReader reads r and puts br back into gobwas's pool once r is
// drained.
type pooledReader struct {
	r  io.Reader
	br *bufio.Reader
}

func (p *pooledReader) Read(b []byte) (int, error) {
	if p.br == nil {
		return 0, io.EOF
	}

	n, err := p.r.Read(b)
	if err == io.EOF {
		gobwas.PutReader(p.br)
		p.br = nil
	}
	return n, err
}
