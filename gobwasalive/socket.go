package gobwasalive

import (
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/RussellLuo/timingwheel"
	gobwas "github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Socket completes the `neffos.Socket` interface,
// it describes the underline websocket connection.
type Socket struct {
	UnderlyingConn net.Conn
	request        *http.Request

	reader         *wsutil.Reader
	controlHandler wsutil.FrameHandlerFunc
	state          gobwas.State

	mu sync.Mutex

	//after idleTime not received any MESSAGE/PING/PONG
	//will send a PING to remote. keepalive
	//set zero will disable this feature
	idleTime time.Duration

	//global timingwheel for better performance of timer
	tw *timingwheel.TimingWheel
}

func newSocket(underline net.Conn, request *http.Request, client bool,
	idleTime time.Duration, tw *timingwheel.TimingWheel) *Socket {
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
		idleTime:       idleTime,
		tw:             tw,
	}
}

// NetConn returns the underline net connection.
func (s *Socket) NetConn() net.Conn {
	return s.UnderlyingConn
}

// Request returns the http request value.
func (s *Socket) Request() *http.Request {
	return s.request
}

const MinPingTime = 10 * time.Second

// ReadData reads binary or text messages from the remote connection.
func (s *Socket) ReadData(timeout time.Duration) ([]byte, error) {
	delayTime := s.idleTime
	var delayTimer *timingwheel.Timer
	for {
		if timeout > 0 {
			if err := s.UnderlyingConn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
				log.Printf("gobwasalive Socket SetReadDeadline, err: %v", err)
			}
			if timeout-MinPingTime < s.idleTime {
				delayTime = MinPingTime
			}
		}

		if delayTime > 0 {
			delayTimer = s.tw.AfterFunc(delayTime, func() {
				//log.Println("gobwasalive Socket Send keepalive websocket PING")
				if err := s.write([]byte(""), gobwas.OpPing, MinPingTime); err != nil {
					log.Printf("gobwasalive Socket Send keepalive websocket PING, err: %v", err)
				}
			})
		}

		hdr, err := s.reader.NextFrame()

		if delayTimer != nil {
			delayTimer.Stop()
			delayTimer = nil
		}

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

		if hdr.OpCode&gobwas.OpBinary == 0 && hdr.OpCode&gobwas.OpText == 0 {
			err = s.reader.Discard()
			if err != nil {
				return nil, err
			}
			continue
		}

		return ioutil.ReadAll(s.reader)
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

// WriteBinary sends a binary message to the remote connection.
func (s *Socket) WriteBinary(body []byte, timeout time.Duration) error {
	return s.write(body, gobwas.OpBinary, timeout)
}

// WriteText sends a text message to the remote connection.
func (s *Socket) WriteText(body []byte, timeout time.Duration) error {
	return s.write(body, gobwas.OpText, timeout)
}

func (s *Socket) write(body []byte, op gobwas.OpCode, timeout time.Duration) error {
	s.mu.Lock()
	if timeout > 0 {
		if err := s.UnderlyingConn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
			log.Printf("gobwasalive Socket SetWriteDeadline, err: %v", err)
		}
	}

	// println("write: " + string(body))
	err := wsutil.WriteMessage(s.UnderlyingConn, s.state, op, body)

	if timeout > 0 { //must set it back to zero, otherwise it will get EOF error
		if err := s.UnderlyingConn.SetWriteDeadline(time.Time{}); err != nil {
			log.Printf("gobwasalive Socket SetWriteDeadline back, err: %v", err)
		}
	}

	s.mu.Unlock()

	return err
}
