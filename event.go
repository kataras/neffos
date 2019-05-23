package neffos

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

type MessageHandlerFunc func(*NSConn, Message) error

var (
	OnNamespaceConnect    = "_OnNamespaceConnect"
	OnNamespaceConnected  = "_OnNamespaceConnected"
	OnNamespaceDisconnect = "_OnNamespaceDisconnect" // if allowed to connect then it's allowed to disconnect as well.
	OnRoomJoin            = "_OnRoomJoin"            // able to check if allowed to join.
	OnRoomJoined          = "_OnRoomJoined"          // able to broadcast messages to room.
	OnRoomLeave           = "_OnRoomLeave"           // able to broadcast bye-bye messages to room.
	OnRoomLeft            = "_OnRoomLeft"            // if allowed to join to a room, then its allowed to leave from it.

	OnAnyEvent = "_OnAnyEvent" // when event no match.
	// OnNativeMessage is fired on incoming native/raw websocket messages.
	// If this event defined then an incoming message can pass the check (it's an invalid message format)
	// with just the Message's Body filled, the Event is "OnNativeMessage" and IsNative always true.
	// This event should be defined under an empty namespace in order this to work.
	OnNativeMessage = "_OnNativeMessage"
)

func IsSystemEvent(event string) bool {
	switch event {
	case OnNamespaceConnect, OnNamespaceConnected, OnNamespaceDisconnect,
		OnRoomJoin, OnRoomJoined, OnRoomLeave, OnRoomLeft:
		return true
	default:
		return false
	}
}

type CloseError struct {
	error
	Code int
}

func (err CloseError) Error() string {
	return fmt.Sprintf("[%d] %s", err.Code, err.error.Error())
}

// IsDisconnectError reports whether the "err" is a timeout or a closed connection error.
func IsDisconnectError(err error) bool {
	if err == nil {
		return false
	}

	return IsCloseError(err) || IsTimeoutError(err)
}

func isManualCloseError(err error) bool {
	if _, ok := err.(CloseError); ok {
		return true
	}

	return false
}

// IsCloseError reports whether the "err" is a "closed by the remote host" network connection error.
func IsCloseError(err error) bool {
	if err == nil {
		return false
	}

	if isManualCloseError(err) {
		return true
	}

	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return true
	}

	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Err == nil {
			return false
		}

		if sysErr, ok := netErr.Err.(*os.SyscallError); ok {
			if sysErr.Err == nil {
				return false
			}
			// return strings.HasSuffix(sysErr.Err.Error(), "closed by the remote host.")
			return true
		}

		return strings.HasSuffix(err.Error(), "use of closed network connection")
	}

	return false
}

// IsTimeoutError reports whether the "err" is caused by a defined timeout.
func IsTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	if netErr, ok := err.(*net.OpError); ok {
		// poll.TimeoutError is the /internal/poll of the go language itself, we can't use it directly.
		return netErr.Timeout()
	}

	return false
}

type reply struct {
	Body []byte
}

func (r reply) Error() string {
	return ""
}

func isReply(err error) ([]byte, bool) {
	if err != nil {
		if r, ok := err.(reply); ok {
			return r.Body, true
		}
	}
	return nil, false
}

func Reply(body []byte) error {
	return reply{body}
}
