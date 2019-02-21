package fastws

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/iris-contrib/go.uuid"

	"github.com/gobwas/ws"
)

type IDGenerator func(*Conn) string

// DefaultIDGenerator returns a random unique for a new connection.
var DefaultIDGenerator IDGenerator = func(_ *Conn) string {
	id, err := uuid.NewV4()
	if err != nil {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return id.String()
}

type FastWS struct {
	IDGenerator IDGenerator
	// CheckOrigin a function that is called right before the handshake,
	// if returns false then that client is not allowed to connect with the websocket server.
	CheckOrigin func(r *http.Request) bool
	// HandshakeTimeout specifies the duration for the handshake to complete.
	// 0 means no timeout.
	HandshakeTimeout time.Duration

	// Defaults to an empty one, can be used
	// to customize the Read and Write buffer sizes
	// and add callbacks like `OnRequest`, `OnHost` and etc.
	// Useful only on `UpgradeTCP`.
	TCPUpgrader ws.Upgrader

	// Events.
	OnUpgrade      func(*Conn) error
	OnConnected    func(*Conn) error
	OnError        func(*Conn) bool // Conn#Err(), if false disconnect if true means that it is handled.
	OnDisconnected func(*Conn)
}

func New() *FastWS {
	return &FastWS{IDGenerator: DefaultIDGenerator}
}

func writeError(w http.ResponseWriter, statusCode int) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(statusCode)
	fmt.Fprintln(w, http.StatusText(statusCode))
}

func (f *FastWS) handleError(c *Conn, err error) bool {
	if err == nil || f.OnError == nil {
		return false
	}

	c.reason = err
	return f.OnError(c)
}

type ErrUpgrade struct {
	error
	Code int
}

func IsUpgrade(err error) bool {
	if err == nil {
		return false
	}

	_, ok := err.(ErrUpgrade)
	return ok
}

func IsDisconnected(err error) bool {
	if err == nil {
		return false
	}

	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Err == nil {
			return false
		}

		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			if sysErr.Err == nil {
				return false
			}
			return strings.HasSuffix(sysErr.Err.Error(), "closed by the remote host.")
		}
	}

	return false
}

func (f *FastWS) UpgradeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// RCF rfc2616 https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
		// The response MUST include an Allow header containing a list of valid methods for the requested resource.
		//
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Allow#Examples
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	if f.CheckOrigin != nil && !f.CheckOrigin(r) {
		writeError(w, http.StatusForbidden)
		return
	}

	c := &Conn{
		Request: r,
		Header:  make(http.Header),
	}
	c.ID = f.IDGenerator(c)

	if f.OnUpgrade != nil {
		err := f.OnUpgrade(c)
		if !f.handleError(c, err) {
			if abort, ok := err.(ErrUpgrade); ok && abort.Code > 0 {
				// this is the only error that we fire back.
				writeError(w, abort.Code)
			}

			return
		}
	}

	upgrader := ws.HTTPUpgrader{
		Header:  c.Header,
		Timeout: f.HandshakeTimeout,
	}

	conn, _, hs, err := upgrader.Upgrade(r, w)
	if err != nil {
		abort := ErrUpgrade{err, http.StatusServiceUnavailable}
		if !f.handleError(c, abort) {
			writeError(w, abort.Code)
		}

		return
	}

	defer func() {
		conn.Close()
		if f.OnDisconnected != nil {
			f.OnDisconnected(c)
		}
	}()

	c.establish(conn, hs, ws.StateServerSide)

	if f.OnConnected != nil {
		err = f.OnConnected(c)
		if !f.handleError(c, err) {
			return
		}
	}
}

func (f *FastWS) UpgradeTCP(conn net.Conn) {
	c := new(Conn)
	c.ID = f.IDGenerator(c)
	c.NetConn = conn

	if f.OnUpgrade != nil {
		err := f.OnUpgrade(c)
		if !f.handleError(c, err) {
			return
		}
	}

	hs, err := f.TCPUpgrader.Upgrade(conn)
	if err != nil {
		f.handleError(c, err)
		return
	}

	defer func() {
		conn.Close()
		if f.OnDisconnected != nil {
			f.OnDisconnected(c)
		}
	}()

	c.establish(conn, hs, ws.StateServerSide)

	if f.OnConnected != nil {
		err = f.OnConnected(c)
		if !f.handleError(c, err) {
			return
		}
	}
}
