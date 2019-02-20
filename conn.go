package websocket

import (
	"encoding/json"
	"encoding/xml"
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
	WriteCode ws.OpCode
	// WriteTimeout time allowed to write a message to the connection.
	// 0 means no timeout.
	WriteTimeout time.Duration

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
}

func (c *Conn) Err() error {
	return c.reason
}

func (c *Conn) Read(b []byte) (n int, err error) {
	if c.ReadTimeout > 0 {
		c.NetConn.SetReadDeadline(time.Now().Add(c.ReadTimeout))
	}

	hdr, err := c.Reader.NextFrame()
	if err != nil {
		return 0, err
	}

	if hdr.OpCode.IsControl() {
		err = c.ControlHandler(hdr, c.Reader)
		return 0, err
	}

	if hdr.OpCode&ws.OpText == 0 && hdr.OpCode&ws.OpBinary == 0 {
		err = c.Reader.Discard()
		return 0, err
	}

	return c.Reader.Read(b)
}

func (c *Conn) Write(b []byte) (int, error) {
	if c.WriteTimeout > 0 {
		c.NetConn.SetWriteDeadline(time.Now().Add(c.WriteTimeout))
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
