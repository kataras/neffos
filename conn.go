package fastws

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Conn is stream-oriented websocket connection.
// Both server and client sides uses it.
// Its fields can be customized after connection established.
type Conn struct { // io.Reader and io.Writer fully compatible, bufio.Scanner can be used.
	// ID is the unique identifier for this Conn, it is only used on server-side and its `Conn.tring()`
	// but callers can use it for higher level features as well.
	//
	// Look at `FastWS#IDGenerator` to change how this string field is generated.
	ID string

	// Request is the underline HTTP request value.
	Request *http.Request
	// Header is a server-side only field.
	// It can be modified on the `OnUpgrade` event to send custom headers on HTTP upgrade.
	Header http.Header

	// After connected successfully.

	// NetConn is available at the `OnConnected` state for server-side and
	// after `Dial` for client-side.
	// It is the underline generic network connection.
	netConn net.Conn
	// Handshake is available for reading at the `OnConnected` state for server-side
	// and after `Dial` for client-side.
	Handshake ws.Handshake
	// State is available for reading at the `OnConnected` state for server-side
	// and after `Dial` for client-side.
	State ws.State

	// These can be customized before each `Read` and `Decode`.

	// ControlHandler is the underline `gobwas/wsutil#FrameHandlerFunc` which
	// can be modified to a custom FrameHandlerFunc before `Read` and `Decode`.
	//
	// It is available for both server and client sides at `OnConnected` and after `Dial`.
	ControlHandler wsutil.FrameHandlerFunc
	// ControlHandler is the underline `gobwas/wsutil#Reader` which
	// can be modified to a custom FrameHandlerFunc before `Read` and `Decode`.
	//
	// It is available for both server and client sides at `OnConnected` and after `Dial`.
	Reader *wsutil.Reader
	// ReadTimeout time allowed to read a message from the connection, can be altered before `Read` and `Decode`.
	// It is available for both server and client sides at `OnConnected` and after `Dial`.
	// Defaults to no timeout.
	ReadTimeout time.Duration

	// These can be customized before each `Write`, `WriteWithCode` and `Encode`.

	// If non-nil its `Write` will be used instead, on `Encode` its `Write` +`Flush`.
	// Defaults to a buffered writer, if nil then it will write all data without buffering.
	Writer    *wsutil.Writer
	Flush     bool // if true and Writer is non-nil (as defaulted) it will call c.Writer.Flush after each .Write. Defaults to true.
	WriteCode ws.OpCode
	mu        sync.Mutex
	// WriteTimeout time allowed to write a message to the connection, can be altered before `Write`, `WriteWithCode` and `Encoder`.
	// Defaults to no timeout.
	WriteTimeout time.Duration

	encoder Encoder
	decoder Decoder

	reason error

	// OnError fires whenever an error returned from `FastWS#OnUpgrade` or `FastWS#OnConnected`.
	// If it is from `OnUpgrade` then the boolean result will define if
	// the connection should be closed (force client disconnect) with "true" or
	// log and ignore the error with "false" if error is not a "closed type" (see below).
	// If it is from `OnConnected` then the result does not really matter.
	// It accepts the  error which contains the last known reason that it raised the `OnError` callback.
	//
	// Look `IsTimeout`, `IsClosed` and `IsDisconnected` error check helpers too,
	// this pattern allows the caller to define its own custom errors and handle them in one place.
	OnError func(err error) bool
}

func (c *Conn) establish(conn net.Conn, hs ws.Handshake, state ws.State) {
	controlHandler := wsutil.ControlFrameHandler(conn, state)

	rd := &wsutil.Reader{
		Source:          conn,
		State:           state,
		CheckUTF8:       true,
		SkipHeaderCheck: false,
		// OnIntermediate:  controlHandler,
	}

	c.netConn = conn
	c.Handshake = hs
	c.State = state
	c.ControlHandler = controlHandler
	c.Reader = rd
	c.WriteCode = ws.OpText // OpBinary
	// c.Writer = wsutil.NewWriter(conn, state, c.WriteCode)
	// c.Writer = wsutil.GetWriter(conn, state, c.WriteCode, 0)
	c.Flush = true
}

func (c *Conn) NetConn() net.Conn {
	return c.netConn
}

// IsClient reports wether this Conn is client side.
func (c *Conn) IsClient() bool {
	return c.State == ws.StateClientSide
}

func (c *Conn) HandleError(err error) bool {
	if err == nil {
		return true
	}

	c.reason = err

	if c.OnError == nil {
		return false
	}

	return c.OnError(err)
}

// Err may return the reason of an error, available at the `OnError` event for server-side.
func (c *Conn) Err() error {
	return c.reason
}

func (c *Conn) String() (s string) {
	if c.netConn == nil {
		return
	}

	if c.ID != "" {
		s = c.ID + " "
	}

	s += "<" + c.netConn.RemoteAddr().String() + ">"
	return
}

type (
	Encoder interface{ Encode(v interface{}) error }
	Decoder interface{ Decode(vPtr interface{}) error }
)

func (c *Conn) SetEncoding(encoder Encoder, decoder Decoder) {
	c.encoder = encoder
	c.decoder = decoder
}

func (c *Conn) Encode(v interface{}) error {
	err := c.encoder.Encode(v)
	if err != nil {
		return err
	}

	if c.Writer != nil && !c.Flush { // Flushed already if c.Flush is true.
		return c.Writer.Flush()
	}

	return nil
}

func (c *Conn) Decode(vPtr interface{}) error {
	return c.decoder.Decode(vPtr)
}

func (c *Conn) applyReadTimeout() {
	if c.ReadTimeout > 0 {
		c.netConn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
	}
}

// Returns io.EOF on remote close.
func (c *Conn) Read(b []byte) (n int, err error) {
	if c.Reader != nil {
		for {
			c.applyReadTimeout()

			hdr, err := c.Reader.NextFrame()
			if err != nil {
				if err == io.EOF {
					return 0, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
				}
				return 0, err
			}

			if hdr.OpCode == ws.OpClose {
				return 0, io.ErrUnexpectedEOF // for io.ReadAll to return an error if connection remotely closed.
			}

			if hdr.OpCode.IsControl() {
				err = c.ControlHandler(hdr, c.Reader)
				if err != nil {
					return 0, err
				}
				continue
			}

			if hdr.OpCode&c.WriteCode == 0 {
				err = c.Reader.Discard()
				if err != nil {
					return 0, err
				}
				continue
			}

			return c.Reader.Read(b)
		}
	}

	for {
		c.applyReadTimeout()

		data, opCode, err := wsutil.ReadData(c.netConn, c.State)
		if err != nil {
			return 0, err
		}

		if opCode&c.WriteCode == 0 { //&& opCode&ws.OpBinary == 0 {
			continue
		}

		n = copy(b, data)
		return n, io.EOF
	}
}

func (c *Conn) Write(b []byte) (int, error) {
	if c.WriteTimeout > 0 {
		c.netConn.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
	}

	if c.Writer != nil {
		// Note if available buffer is smaller than len(b) then it will write through all.
		n, err := c.Writer.Write(b)
		// n, err := c.Writer.WriteThrough(b)
		if err != nil {
			return 0, err
		}

		if c.Flush {
			err = c.Writer.Flush()
			c.Writer.Reset(c.netConn, c.State, c.WriteCode)
			if err != nil {
				return 0, err
			}
		}

		return n, err
	}

	// if c.Writer != nil {
	// 	err := ws.WriteHeader(c.netConn, ws.Header{
	// 		Fin:    false,
	// 		Length: int64(len(b)),
	// 		Masked: c.IsClient(),
	// 		OpCode: c.WriteCode,
	// 	})

	// 	if err != nil {
	// 		// fmt.Printf("error while writing the write header: %v\n", err)
	// 		return 0, err
	// 	}

	// 	return c.netConn.Write(b)
	// }

	err := wsutil.WriteMessage(c.netConn, c.State, c.WriteCode, b)
	if err != nil {
		return 0, err
	}

	return len(b), nil

	// c.Writer.Reset(c.netConn, c.State, ws.OpText)
	//
	// n, err := c.Writer.Write(b)
	// if err != nil {
	// 	return 0, err
	// }
	// err = c.Writer.Flush()
	// if err != nil {
	// 	return 0, err
	// }
	//
	//	return n, err
}

// OpCode a type alias for `ws#OpCode`.
type OpCode = ws.OpCode

// Operation codes defined by specification.
// See https://tools.ietf.org/html/rfc6455#section-5.2
const (
	// OpText denotes a text data message. The text message payload is
	// interpreted as UTF-8 encoded text data.
	OpText OpCode = ws.OpText
	// OpBinary denotes a binary data message.
	OpBinary OpCode = ws.OpBinary
	// OpClose denotes a close control message.
	OpClose OpCode = ws.OpClose

	// OpPing denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	OpPing OpCode = ws.OpPing
	// OpPong denotes a ping control message. The optional message payload
	// is UTF-8 encoded text.
	OpPong OpCode = ws.OpPong
)

// WriteWithCode writes to the connection by passing bypasses the `Writer` and `WriterCode`.
func (c *Conn) WriteWithCode(opCode OpCode, b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.WriteTimeout > 0 {
		c.netConn.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
	}

	err := wsutil.WriteMessage(c.netConn, c.State, opCode, b)
	if err != nil {
		return 0, err
	}

	return len(b), nil
}

func (c *Conn) WriteText(body []byte, timeout time.Duration) error {
	_, err := c.WriteWithCode(OpText, body)
	return err
}

func (c *Conn) ReadBinary() ([]byte, error) {
	return ioutil.ReadAll(c)
}

func (c *Conn) ReadText(timeout time.Duration) ([]byte, error) {
	return c.ReadBinary()
}

func (c *Conn) ReadJSON(vPtr interface{}) error {
	b, err := c.ReadBinary()
	if err != nil {
		return err
	}

	return json.Unmarshal(b, vPtr)
}

func (c *Conn) ReadXML(vPtr interface{}) error {
	b, err := c.ReadBinary()
	if err != nil {
		return err
	}

	return xml.Unmarshal(b, vPtr)
}

func (c *Conn) WriteJSON(v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.Write(b)
	return err
}

func (c *Conn) WriteXML(v interface{}) error {
	b, err := xml.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.Write(b)
	return err
}

func (c *Conn) Close() error {
	// wsutil.PutWriter(c.Writer)
	return c.netConn.Close()
}
