package gobwas

import (
	"io"
	"io/ioutil"
	"net"
	"sync"
	"time"

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type Socket struct {
	UnderlyingConn net.Conn

	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	state          gobwas.State

	mu sync.Mutex
}

func newSocket(underline net.Conn, client bool) *Socket {
	state := gobwas.StateServerSide
	if client {
		state = gobwas.StateClientSide
	}

	controlHandler := wsutil.ControlFrameHandler(underline, state)

	reader := &wsutil.Reader{
		Source:          underline,
		State:           state,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		OnIntermediate:  controlHandler,
	}

	return &Socket{
		UnderlyingConn: underline,
		state:          state,
		reader:         reader,
		controlHandler: controlHandler,
	}
}

func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn
}

// Returns io.EOF on remote close.
func (s *Socket) ReadText(timeout time.Duration) ([]byte, error) {
	for {
		if timeout > 0 {
			s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
		}

		hdr, err := s.reader.NextFrame()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
			}
			return nil, err
		}

		if hdr.OpCode == gobwas.OpClose {
			return nil, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
		}

		if hdr.OpCode.IsControl() {
			err = s.controlHandler(hdr, s.reader)
			if err != nil {
				return nil, err
			}
			continue
		}

		if hdr.OpCode&gobwas.OpText == 0 {
			err = s.reader.Discard()
			if err != nil {
				return nil, err
			}
			continue
		}

		return ioutil.ReadAll(s.reader)
	}
}

func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	if timeout > 0 {
		s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout))
	}

	s.mu.Lock()
	err := wsutil.WriteMessage(s.UnderlyingConn, s.state, gobwas.OpText, body)
	s.mu.Unlock()

	return err
}
