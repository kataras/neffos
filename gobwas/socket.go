package gobwas

import (
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type Socket struct {
	UnderlyingConn net.Conn
	request        *http.Request

	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	state          gobwas.State

	mu sync.Mutex
}

const defaultOp = gobwas.OpBinary

func newSocket(underline net.Conn, request *http.Request, client bool) *Socket {
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
		// "intermediate" frames, that possibly could
		// be received between text/binary continuation frames.
		// Read `gobwas/wsutil/reader#NextReader`.
		//
		OnIntermediate: controlHandler,
	}

	return &Socket{
		UnderlyingConn: underline,
		request:        request,
		state:          state,
		reader:         reader,
		controlHandler: controlHandler,
	}
}

func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn
}

func (s *Socket) Request() *http.Request {
	return s.request
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

		if hdr.OpCode&defaultOp == 0 {
			err = s.reader.Discard()
			if err != nil {
				return nil, err
			}
			continue
		}

		// s.readerSync.Lock()
		b, err := ioutil.ReadAll(s.reader)
		// println("read: " + string(b))
		return b, err
	}

	// for {
	// 	if timeout > 0 {
	// 		s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout))
	// 	}

	// 	b, code, err := wsutil.ReadData(s.UnderlyingConn, s.state)
	// 	if err != nil {
	// 		return nil, err
	// 	}

	// 	if code != defaultOp {
	// 		continue
	// 	}

	// 	return b, nil
	// }
}

func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	s.mu.Lock()
	if timeout > 0 {
		s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout))
	}

	// println("write: " + string(body))
	err := wsutil.WriteMessage(s.UnderlyingConn, s.state, defaultOp, body)
	s.mu.Unlock()

	return err
}
