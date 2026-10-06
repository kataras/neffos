package neffos

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"unicode/utf8"
)

// MessageHandlerFunc is the definition type of the events' callback.
// Its error can be written to the other side on specific events,
// i.e on `OnNamespaceConnect` it will abort a remote namespace connection.
// See examples for more.
type MessageHandlerFunc func(*NSConn, Message) error

const (
	// OnNamespaceConnect is the event name which its callback is fired right before namespace connect,
	// if non-nil error then the remote connection's `Conn.Connect` will fail and send that error text.
	// Connection is not ready to emit data to the namespace.
	OnNamespaceConnect = "_OnNamespaceConnect"
	// OnNamespaceConnected is the event name which its callback is fired after namespace successfully connected.
	// Connection is ready to emit data back to the namespace.
	OnNamespaceConnected = "_OnNamespaceConnected"
	// OnNamespaceDisconnect is the event name which its callback is fired when
	// remote namespace disconnection or local namespace disconnection is happening.
	// For server-side connections the reply matters, so if error returned then the client-side cannot disconnect yet,
	// for client-side the return value does not matter.
	OnNamespaceDisconnect = "_OnNamespaceDisconnect" // if allowed to connect then it's allowed to disconnect as well.
	// OnRoomJoin is the event name which its callback is fired right before room join.
	OnRoomJoin = "_OnRoomJoin" // able to check if allowed to join.
	// OnRoomJoined is the event name which its callback is fired after the connection has successfully joined to a room.
	OnRoomJoined = "_OnRoomJoined" // able to broadcast messages to room.
	// OnRoomLeave is the event name which its callback is fired right before room leave.
	OnRoomLeave = "_OnRoomLeave" // able to broadcast bye-bye messages to room.
	// OnRoomLeft is the event name which its callback is fired after the connection has successfully left from a room.
	OnRoomLeft = "_OnRoomLeft" // if allowed to join to a room, then its allowed to leave from it.
	// OnAnyEvent is the event name which its callback is fired when incoming message's event is not declared to the ConnHandler(`Events` or `Namespaces`).
	OnAnyEvent = "_OnAnyEvent" // when event no match.
	// OnNativeMessage is fired on incoming native/raw websocket messages.
	// If this event defined then an incoming message can pass the check (it's an invalid message format)
	// with just the Message's Body filled, the Event is "OnNativeMessage" and IsNative always true.
	// This event should be defined under an empty namespace in order this to work.
	OnNativeMessage = "_OnNativeMessage"
)

// IsSystemEvent reports whether the "event" is a system event,
// OnNamespaceConnect, OnNamespaceConnected, OnNamespaceDisconnect,
// OnRoomJoin, OnRoomJoined, OnRoomLeave and OnRoomLeft.
func IsSystemEvent(event string) bool {
	switch event {
	case OnNamespaceConnect, OnNamespaceConnected, OnNamespaceDisconnect,
		OnRoomJoin, OnRoomJoined, OnRoomLeave, OnRoomLeft:
		return true
	default:
		return false
	}
}

// CloseError can be used to send and close a remote connection in the event callback's return statement.
//
// Code is the close code. Reason is a human-readable explanation, used as the
// error text when there is no underlying error. A literal such as
// CloseError{Code: 1008} is valid.
type CloseError struct {
	error
	Code   int
	Reason string
}

// Error returns "[Code] text", where text is the underlying error's message
// or, without one, the Reason.
func (err CloseError) Error() string {
	if err.error != nil {
		return fmt.Sprintf("[%d] %s", err.Code, err.error.Error())
	}

	return fmt.Sprintf("[%d] %s", err.Code, err.Reason)
}

// Unwrap returns the underlying error, if any, so errors.Is and errors.As
// can see through a CloseError.
func (err CloseError) Unwrap() error {
	return err.error
}

// Close status codes from RFC 6455 section 7.4 and the IANA registry. Pass
// them to Conn.Terminate or return them in a CloseError, and compare them
// with the result of CloseStatus.
const (
	CloseNormalClosure           = 1000
	CloseGoingAway               = 1001
	CloseProtocolError           = 1002
	CloseUnsupportedData         = 1003
	CloseNoStatusReceived        = 1005
	CloseAbnormalClosure         = 1006
	CloseInvalidFramePayloadData = 1007
	ClosePolicyViolation         = 1008
	CloseMessageTooBig           = 1009
	CloseMandatoryExtension      = 1010
	CloseInternalServerErr       = 1011
	CloseServiceRestart          = 1012
	CloseTryAgainLater           = 1013
	CloseTLSHandshake            = 1015
)

// CloseStatus returns the close code of err when err is or wraps a
// CloseError, and -1 otherwise.
func CloseStatus(err error) int {
	if ce, ok := errors.AsType[CloseError](err); ok {
		return ce.Code
	}

	return -1
}

// maxCloseReasonLen is the longest close reason that fits in a close frame:
// 125 bytes of control frame payload minus the 2-byte code.
const maxCloseReasonLen = 123

// closeFrame returns the code and reason to send in a close frame for err.
// A CloseError gives its own code and its Reason, or its underlying error's
// text when Reason is empty. Any other error closes normally with no reason.
// The reason is cut to maxCloseReasonLen bytes on a UTF-8 boundary.
func closeFrame(err error) (int, string) {
	ce, ok := errors.AsType[CloseError](err)
	if !ok {
		return CloseNormalClosure, ""
	}

	reason := ce.Reason
	if reason == "" && ce.error != nil {
		reason = ce.error.Error()
	}

	return ce.Code, truncateCloseReason(reason)
}

func truncateCloseReason(reason string) string {
	if len(reason) <= maxCloseReasonLen {
		return reason
	}

	n := maxCloseReasonLen
	for n > 0 && !utf8.RuneStart(reason[n]) {
		n--
	}
	return reason[:n]
}

// ErrMessageTooBig is the error Conn.Err reports after the remote side sent a
// message bigger than WithTimeout.MaxMessageSize.
var ErrMessageTooBig = errors.New("message too big")

// IsDisconnectError reports whether the "err" is a timeout or a closed connection error.
func IsDisconnectError(err error) bool {
	if err == nil {
		return false
	}

	return IsCloseError(err) || IsTimeoutError(err)
}

func isManualCloseError(err error) bool {
	_, ok := errors.AsType[CloseError](err)
	return ok
}

// IsCloseError reports whether the "err" is a "closed by the remote host" network connection error.
func IsCloseError(err error) bool {
	if err == nil {
		return false
	}

	if isManualCloseError(err) {
		return true
	}

	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}

	// "use of closed network connection", wrapped or not.
	if errors.Is(err, net.ErrClosed) {
		return true
	}

	netErr, ok := errors.AsType[*net.OpError](err)
	if !ok || netErr.Err == nil {
		return false
	}

	_, ok = errors.AsType[*os.SyscallError](netErr.Err)
	return ok
}

// IsTimeoutError reports whether the "err" is caused by a defined timeout:
// a net.Error whose Timeout method reports true, os.ErrDeadlineExceeded or
// context.DeadlineExceeded, wrapped or not.
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	netErr, ok := errors.AsType[net.Error](err)
	return ok && netErr.Timeout()
}

type reply struct {
	Body []byte
}

func (r reply) Error() string {
	return ""
}

func isReply(err error) ([]byte, bool) {
	if err == nil {
		return nil, false
	}
	if r, ok := errors.AsType[reply](err); ok {
		return r.Body, true
	}
	return nil, false
}

// Reply is a special type of custom error which sends a message back to the other side
// with the exact same incoming Message's Namespace (and Room if specified)
// except its body which would be the given "body".
func Reply(body []byte) error {
	return reply{body}
}

// ReplyObject is `Reply` for a value: it encodes "v" with `Marshal` and replies
// with the result. When encoding fails the error itself is returned, so the
// caller's event callback reports it instead of sending a broken body.
func ReplyObject(v any) error {
	body, err := Marshal(v)
	if err != nil {
		return err
	}

	return Reply(body)
}
