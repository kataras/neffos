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

// IDGenerator is the type of function that it is used
// to generate unique identifiers for new connections.
//
// See `FastWS.IDGenerator`.
type IDGenerator func(*Conn) string

// DefaultIDGenerator returns a random unique for a new connection.
var DefaultIDGenerator IDGenerator = func(_ *Conn) string {
	id, err := uuid.NewV4()
	if err != nil {
		return strconv.FormatInt(time.Now().Unix(), 10)
	}
	return id.String()
}

// FastWS is the main component of the package.
// It represents the websocket server.
// This websocket server can handle both HTTP and raw TCP connections (zero-upgrade).
// The only required field that should be filled is the `OnConnected`, the rest are optionally.
//
// Its main methods are `UpgradeHTTP` and `UpgradeTCP`.
type FastWS struct {
	// IDGenerator is used to generate unique identifier for each new incoming websocket client.
	// By default it will generate random UUID.
	// See `Conn.ID` for more.
	IDGenerator IDGenerator
	// CheckOrigin a function that is called right before the handshake,
	// if returns false then client is not allowed to connect and 403 forbidden is replied.
	CheckOrigin func(r *http.Request) bool
	// HandshakeTimeout specifies the duration for the handshake to complete.
	// Defaults to no timeout.
	HandshakeTimeout time.Duration
	// Defaults to an empty one, can be used
	// to customize the Read and Write buffer sizes
	// and add callbacks like `OnRequest`, `OnHost` and etc.
	// Useful only on `Upgrade`.
	Upgrader ws.Upgrader

	// OnUpgrade runs before the upgrade process, it is the first event that is called.
	// From which you can customize the Conn's properties.
	// If the result is non-nil error then it aborts the process
	// and, when HTTP, sends a 403 error
	// to the client(Use `ErrUpgrade.Code` to customize the error type),
	// unless the `OnError` handles it and returned true.
	OnUpgrade func(*Conn) error
	// OnConnected is notified about new incoming websocket clients.
	// It is the most critical callback, it runs when a new client is connected, after `OnUpgrade`.
	// It accepts the connected `Conn`
	// and may return a non-nil error to be passed on the `OnError` callback.
	// When this callback exits the connection with this client is terminated,
	// most common case is to perform a for loop and read, send messages.
	OnConnected func(*Conn) error
	// OnError fires whenever an error returned from `OnUpgrade` or `OnConnected`.
	// If it is from `OnUpgrade` then the boolean result will define if
	// the connection should be closed (force client disconnect) with "true" or log and ignore the error with "false".
	// If it is from `OnConnected` then the result does not really matter.
	// It accepts the `Conn`, the error is the `Conn.Err()` which contains the last known reason that it raised the `OnError` callback.
	//
	// Look `IsTimeout`, `IsClosed` and `IsDisconnected` error check helpers too,
	// this pattern allows the caller to define its own custom errors and handle them in one place.
	OnError func(*Conn) bool // Conn#Err(), if false disconnect if true means that it is handled.
}

// New returns a new websocket server, read `FastWS` struct type docs for more.
func New() *FastWS {
	return &FastWS{IDGenerator: DefaultIDGenerator}
}

func writeError(w http.ResponseWriter, statusCode int) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(statusCode)
	fmt.Fprintln(w, http.StatusText(statusCode))
}

func (fws *FastWS) HandleError(c *Conn, err error) bool {
	if err == nil || fws.OnError == nil {
		return true
	}

	c.reason = err
	return fws.OnError(c)
}

type ErrUpgrade struct {
	error
	Code int
}

// IsUpgrade reports whether the "err" is an `ErrUpgrade`.
func IsUpgrade(err error) bool {
	if err == nil {
		return false
	}

	_, ok := err.(ErrUpgrade)
	return ok
}

// IsDisconnected reports whether the "err" is a timeout or a closed connection error.
func IsDisconnected(err error) bool {
	if err == nil {
		return false
	}

	return IsClosed(err) || IsTimeout(err)
}

// IsClosed reports whether the "err" is a "closed by the remote host" network connection error.
func IsClosed(err error) bool {
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

		return strings.HasSuffix(err.Error(), "use of closed network connection")
	}

	return false
}

// IsTimeout reports whether the "err" is caused by a defined timeout,
// callers can add timeouts via `Dial` or via `Conn.WriteTimeout`, `Conn.ReadTimeout`, `FastWS.HandshakeTimeout`.
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}

	if netErr, ok := err.(*net.OpError); ok {
		// poll.TimeoutError is the /internal/poll of the go language itself, we can't use it directly.
		return netErr.Timeout()
	}

	return false
}

// UpgradeHTTP upgrades HTTP connection to the websocket connection.
//
// The HTTP Method should be "GET".
// If `CheckOrigin` exists, then it should be passed otherwise it sends 403 error code.
// It calls `OnUpgrade` before upgrade, Upgrade action happens and if succeed,
// `OnConnected` is called and `OnDisconnected` on exit.
func (fws *FastWS) UpgradeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// RCF rfc2616 https://www.w3.org/Protocols/rfc2616/rfc2616-sec10.html
		// The response MUST include an Allow header containing a list of valid methods for the requested resource.
		//
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Allow#Examples
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed)
		return
	}

	if fws.CheckOrigin != nil && !fws.CheckOrigin(r) {
		writeError(w, http.StatusForbidden)
		return
	}

	c := &Conn{
		Request: r,
		Header:  make(http.Header),
	}
	c.ID = fws.IDGenerator(c)

	if fws.OnUpgrade != nil {
		err := fws.OnUpgrade(c)
		if !fws.HandleError(c, err) {
			if abort, ok := err.(ErrUpgrade); ok && abort.Code > 0 {
				// this is the only error that we fire back.
				writeError(w, abort.Code)
			} else {
				writeError(w, http.StatusBadRequest)
			}

			return
		}
	}

	upgrader := ws.HTTPUpgrader{
		Header:  c.Header,
		Timeout: fws.HandshakeTimeout,
	}

	conn, _, hs, err := upgrader.Upgrade(r, w)
	if err != nil {
		abort := ErrUpgrade{err, http.StatusServiceUnavailable}
		if !fws.HandleError(c, abort) {
			writeError(w, abort.Code)
		}

		return
	}

	c.establish(conn, hs, ws.StateServerSide)

	if fws.OnConnected != nil {
		err = fws.OnConnected(c)
		if err != c.reason { // sometimes the user may want to call the `HandleError` manually, we don't want to push the same error twice.
			if !fws.HandleError(c, err) {
				return
			}
		}
	}

}

// Upgrade zero-copy upgrades connection to websocket.
// It interprets given conn as connection with incoming HTTP Upgrade request.
// It is a caller responsibility to manage i/o timeouts on conn.
//
// It calls `OnUpgrade` before upgrade, Upgrade action happens and if succeed,
// `OnConnected` is called and `OnDisconnected` on exit.
func (fws *FastWS) Upgrade(conn net.Conn) {
	c := new(Conn)
	c.ID = fws.IDGenerator(c)
	c.NetConn = conn

	if fws.OnUpgrade != nil {
		err := fws.OnUpgrade(c)
		if !fws.HandleError(c, err) {
			return
		}
	}

	hs, err := fws.Upgrader.Upgrade(conn)
	if err != nil {
		fws.HandleError(c, err)
		return
	}

	c.establish(conn, hs, ws.StateServerSide)

	if fws.OnConnected != nil {
		err = fws.OnConnected(c)
		if err != c.reason { // sometimes the user may want to call the `HandleError` manually, we don't want to push the same error twice.
			if !fws.HandleError(c, err) {
				return
			}
		}
	}
}
