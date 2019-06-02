package neffos

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// The Message is the structure which describes the incoming and outcoming data.
// Emitter's "body" argument is the `Message.Body` field.
// Emitter's return non-nil error is the `Message.Err` field.
// If native message sent then the `Message.Body` is filled with the body and
// when incoming native message then the `Message.Event` is the `OnNativeMessage`,
// native messages are allowed only when an empty namespace("") and its `OnNativeMessage` callback are present.
//
// The the raw data received/sent structured following this order:
// <wait()>;
// <namespace>;
// <room>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
//
// Internal `serializeMessage` and `deserializeMessage` functions
// do the job on `Conn#Write`, `NSConn#Emit` and `Room#Emit` calls.
type Message struct {
	wait string

	// The Namespace that this message sent to/received from.
	Namespace string
	// The Room that this message sent to/received from.
	Room string
	// The Event that this message sent to/received from.
	Event string
	// The actual body of the incoming/outcoming data.
	Body []byte
	// The Err contains any message's error, if any.
	// Note that server-side and client-side connections can return an error instead of a message from each event callbacks,
	// except the clients's force Disconnect which its local event doesn't matter when disconnected manually.
	Err error

	// if true then `Err` is filled by the error message and
	// the last segment of incoming/outcoming serialized message is the error message instead of the body.
	isError bool
	isNoOp  bool

	isInvalid bool

	// the CONN ID, filled automatically if `Server#Broadcast` first parameter of sender connection's ID is not empty, not exposed to the subscribers (rest of the clients).
	from string

	// True when event came from local (i.e client if running client) on force disconnection,
	// i.e OnNamespaceDisconnect and OnRoomLeave when closing a conn.
	// This field is not filled on sending/receiving.
	// Err does not matter and never sent to the other side.
	IsForced bool
	// True when asking the other side and fire the respond's event (which matches the sent for connect/disconnect/join/leave),
	// i.e if a client (or server) onnection want to connect
	// to a namespace or join to a room.
	// Should be used rarely, state can be checked by `Conn#IsClient() bool`.
	// This field is not filled on sending/receiving.
	IsLocal bool

	// True when user define it for writing, only its body is written as raw native websocket message, namespace, event and all other fields are empty.
	// The receiver should accept it on the `OnNativeMessage` event.
	// This field is not filled on sending/receiving.
	IsNative bool

	// Useful rarely internally on `Conn#Write` namespace and rooms checks, i.e `Conn#DisconnectAll` and `NSConn#RemoveAll`.
	// If true then the writer's checks will not lock connectedNamespacesMutex or roomsMutex again. May be useful in the future, keep that solution.
	locked bool

	// if server or client should write using Binary message.
	// This field is not filled on sending/receiving.
	SetBinary bool
}

func (m *Message) isConnect() bool {
	return m.Event == OnNamespaceConnect
}

func (m *Message) isDisconnect() bool {
	return m.Event == OnNamespaceDisconnect
}

func (m *Message) isRoomJoin() bool {
	return m.Event == OnRoomJoin
}

func (m *Message) isRoomLeft() bool {
	return m.Event == OnRoomLeft
}

const (
	waitIsConfirmationPrefix  = '#'
	waitComesFromClientPrefix = '$'
)

func (m *Message) isWait(isClientConn bool) bool {
	if m.wait == "" {
		return false
	}

	if m.wait[0] == waitIsConfirmationPrefix {
		// true even if it's not client-client but it's a confirmation message.
		return true
	}

	if m.wait[0] == waitComesFromClientPrefix {
		if isClientConn {
			return true
		}
		return false
	}

	return true
}

func genWait(isClientConn bool) string {
	now := time.Now().UnixNano()
	wait := strconv.FormatInt(now, 10)
	if isClientConn {
		wait = string(waitIsConfirmationPrefix) + wait
	}

	return wait
}

func genWaitConfirmation(wait string) string {
	return string(waitIsConfirmationPrefix) + wait
}

type (
	// MessageEncrypt type kept for future use when serializing a message.
	MessageEncrypt func(out []byte) []byte
	// MessageDecrypt type kept for future use when deserializing a message.
	MessageDecrypt func(in []byte) []byte
)

var (
	trueByte  = []byte{'1'}
	falseByte = []byte{'0'}

	messageSeparatorString = ";"
	messageSeparator       = []byte(messageSeparatorString)
	// we use this because has zero chance to be part of end-developer's Message.Namespace, Room, Event, To and Err fields,
	// semicolon has higher probability to exists on those values. See `escape` and `unescape`.
	messageFieldSeparatorReplacement = "@%!semicolon@%!"
)

// called on `serializeMessage` to all message's fields except the body (and error).
func escape(s string) string {
	if len(s) == 0 {
		return s
	}

	return strings.Replace(s, messageSeparatorString, messageFieldSeparatorReplacement, -1)
}

// called on `deserializeMessage` to all message's fields except the body (and error).
func unescape(s string) string {
	if len(s) == 0 {
		return s
	}

	return strings.Replace(s, messageFieldSeparatorReplacement, messageSeparatorString, -1)
}

func serializeMessage(encrypt MessageEncrypt, msg Message) (out []byte) {
	if msg.IsNative && msg.wait == "" {
		out = msg.Body
	} else {
		out = serializeOutput(msg.wait, escape(msg.Namespace), escape(msg.Room), escape(msg.Event), msg.Body, msg.Err, msg.isNoOp)
	}

	if encrypt != nil {
		out = encrypt(out)
	}

	return out
}

func serializeOutput(wait, namespace, room, event string,
	body []byte,
	err error,
	isNoOp bool,
) []byte {

	var (
		isErrorByte = falseByte
		isNoOpByte  = falseByte
		waitByte    = []byte{}
	)

	if err != nil {
		if b, ok := isReply(err); ok {
			body = b
		} else {
			body = []byte(err.Error())
			isErrorByte = trueByte
		}
	}

	if isNoOp {
		isNoOpByte = trueByte
	}

	if wait != "" {
		waitByte = []byte(wait)
	}

	msg := bytes.Join([][]byte{ // this number of fields should match the deserializer's, see `validMessageSepCount`.
		waitByte,
		[]byte(namespace),
		[]byte(room),
		[]byte(event),
		isErrorByte,
		isNoOpByte,
		body,
	}, messageSeparator)

	return msg
}

// when allowNativeMessages only Body is filled and check about message format is skipped.
func deserializeMessage(decrypt MessageDecrypt, b []byte, allowNativeMessages bool) Message {
	if decrypt != nil {
		b = decrypt(b)
	}

	wait, namespace, room, event, body, err, isNoOp, isInvalid := deserializeInput(b, allowNativeMessages)
	return Message{
		wait,
		unescape(namespace),
		unescape(room),
		unescape(event),
		body,
		err,
		err != nil,
		isNoOp,
		isInvalid,
		"",
		false,
		false,
		allowNativeMessages && event == OnNativeMessage,
		false,
		false,
	}
}

const validMessageSepCount = 7

func deserializeInput(b []byte, allowNativeMessages bool) ( // go-lint: ignore line
	wait,
	namespace,
	room,
	event string,
	body []byte,
	err error,
	isNoOp bool,
	isInvalid bool,
) {

	if len(b) == 0 {
		isInvalid = true
		return
	}

	// Note: Go's SplitN returns the remainder in[6] but JavasSript's string.split behaves differently.
	dts := bytes.SplitN(b, messageSeparator, validMessageSepCount)
	if len(dts) != validMessageSepCount {
		if !allowNativeMessages {
			isInvalid = true
			return
		}

		event = OnNativeMessage
		body = b
		return
	}

	wait = string(dts[0])
	namespace = string(dts[1])
	room = string(dts[2])
	event = string(dts[3])
	isError := bytes.Equal(dts[4], trueByte)
	isNoOp = bytes.Equal(dts[5], trueByte)
	if b := dts[6]; len(b) > 0 {
		if isError {
			errorText := string(b)
			switch errorText {
			case ErrBadNamespace.Error():
				err = ErrBadNamespace
			case ErrBadRoom.Error():
				err = ErrBadRoom
			default:
				err = errors.New(errorText)
			}

		} else {
			body = b // keep it like that.
		}
	}

	return
}

func genEmptyReplyToWait(wait string) []byte {
	return append([]byte(wait), bytes.Repeat(messageSeparator, validMessageSepCount-1)...)
}
