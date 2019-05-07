package ws

import (
	"bytes"
	"errors"
)

// <wait(0-uint64)>;
// <namespace>;
// <room>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
type Message struct {
	wait string

	Namespace string
	Room      string
	Event     string
	Body      []byte
	Err       error

	// if true then `Err` is filled by the error message and
	// the last segment of incoming/outcoming serialized message is the error message instead of the body.
	isError bool
	isNoOp  bool

	isInvalid bool

	from string // the CONN ID, filled automatically.

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

type (
	MessageEncrypt func(out []byte) []byte
	MessageDecrypt func(in []byte) []byte
)

var (
	trueByte  = []byte{'1'}
	falseByte = []byte{'0'}

	messageSeparator = []byte{';'}
)

func serializeMessage(encrypt MessageEncrypt, msg Message) (out []byte) {
	out = serializeOutput(msg.wait, msg.Namespace, msg.Room, msg.Event, msg.Body, msg.Err, msg.isNoOp)

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

	msg := bytes.Join([][]byte{
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

func deserializeMessage(decrypt MessageDecrypt, b []byte) Message {
	if decrypt != nil {
		b = decrypt(b)
	}

	wait, namespace, room, event, body, err, isNoOp, isInvalid := deserializeInput(b)
	return Message{
		wait,
		namespace,
		room,
		event,
		body,
		err,
		err != nil,
		isNoOp,
		isInvalid,
		"",
		false,
		false,
	}
}

func deserializeInput(b []byte) (
	wait,
	namespace,
	room,
	event string,
	body []byte,
	err error,
	isNoOp bool,
	isInvalid bool,
) {

	dts := bytes.SplitN(b, messageSeparator, 7)
	if len(dts) != 7 {
		isInvalid = true
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
			case ErrForbiddenNamespace.Error():
				err = ErrForbiddenNamespace
			default:
				err = errors.New(errorText)
			}

		} else {
			body = b // keep it like that.
		}
	}

	return
}
