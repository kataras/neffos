package fastws

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type Conn struct { // io.Reader and io.Writer fully compatible, bufio.Scanner can be used.
	ID string

	Request *http.Request
	Header  http.Header

	// After connected succesfuly.
	NetConn   net.Conn
	Handshake ws.Handshake
	State     ws.State

	// These can be customized before each `Read`.
	ControlHandler wsutil.FrameHandlerFunc
	Reader         *wsutil.Reader
	// ReadTimeout time allowed to read a message from the connection.
	// 0 means no timeout.
	ReadTimeout time.Duration

	// These can be customized before each `Write`.

	// If not nil its `Write` will be used instead, on `Encode` its `Write` +`Flush`.
	// Defaults to a buffered writer, if nil then it will write all data without buffering.
	Writer    *wsutil.Writer
	WriteCode ws.OpCode
	// WriteTimeout time allowed to write a message to the connection.
	// 0 means no timeout.
	WriteTimeout time.Duration

	encoder Encoder
	decoder Decoder

	reason error
}

func (c *Conn) establish(conn net.Conn, hs ws.Handshake, state ws.State) {
	controlHandler := wsutil.ControlFrameHandler(conn, state)
	rd := &wsutil.Reader{
		Source:          conn,
		State:           state,
		CheckUTF8:       false,
		SkipHeaderCheck: false,
		OnIntermediate:  controlHandler,
	}

	c.NetConn = conn
	c.Handshake = hs
	c.State = state
	c.ControlHandler = controlHandler
	c.Reader = rd
	c.WriteCode = ws.OpText
	c.Writer = wsutil.NewWriter(conn, state, c.WriteCode)
}

func (c *Conn) Err() error {
	return c.reason
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

	if c.Writer != nil {
		return c.Writer.Flush()
	}

	return nil
}

func (c *Conn) Decode(vPtr interface{}) error {
	return c.decoder.Decode(vPtr)
}

// Returns io.EOF on remote close.
func (c *Conn) Read(b []byte) (n int, err error) {
	if c.ReadTimeout > 0 {
		c.NetConn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
	}

readstep:
	hdr, err := c.Reader.NextFrame()
	if err != nil {
		return 0, err
	}

	if hdr.OpCode == ws.OpClose {
		return 0, io.EOF
	}

	if hdr.OpCode.IsControl() {
		err = c.ControlHandler(hdr, c.Reader)
		if err != nil {
			return 0, err
		}
		goto readstep
	}

	if hdr.OpCode&ws.OpText == 0 && hdr.OpCode&ws.OpBinary == 0 {
		err = c.Reader.Discard()
		if err != nil {
			return 0, err
		}
		goto readstep
	}

	return c.Reader.Read(b)
}

func (c *Conn) Write(b []byte) (int, error) {
	if c.WriteTimeout > 0 {
		c.NetConn.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
	}

	if c.Writer != nil {
		return c.Writer.Write(b)
	}

	err := wsutil.WriteMessage(c.NetConn, c.State, c.WriteCode, b)
	if err != nil {
		return 0, err
	}

	return len(b), nil
}

func (c *Conn) ReadBinary() ([]byte, error) {
	return ioutil.ReadAll(c)
}

func (c *Conn) ReadText() string {
	b, err := c.ReadBinary()
	if err != nil {
		return ""
	}

	return string(b)
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
