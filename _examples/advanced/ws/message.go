package ws

import (
	"bytes"
	"errors"
)

type Message struct { // <namespace>;<event>;<isError(0-1)>;<isConnect(0-1)>;<isDisconnect(0-1)>;<body||error_message>
	Namespace string
	Event     string
	Body      []byte
	Err       error

	// if true then `Err` is filled by the error message and
	// the last segment of incoming/outcoming serialized message is the error message instead of the body.
	isError bool

	isConnect    bool
	isDisconnect bool
	isInvalid    bool

	from string // the CONN ID, filled automatically.
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
	out = serializeOutput(msg.Namespace, msg.Event, msg.Body, msg.Err, msg.isConnect, msg.isDisconnect)

	if encrypt != nil {
		out = encrypt(out)
	}
	return out
}

// <namespace>;<event>;<body>
func serializeOutput(namespace string,
	event string,
	body []byte,
	err error,
	isConnect bool,
	isDisconnect bool) []byte {

	var (
		isErrorByte      = falseByte
		isConnectByte    = falseByte
		isDisconnectByte = falseByte
	)

	if err != nil {
		body = []byte(err.Error())
		isErrorByte = trueByte
	}

	if isConnect {
		isConnectByte = trueByte
	}

	if isDisconnect {
		isDisconnectByte = trueByte
	}

	return bytes.Join([][]byte{
		[]byte(namespace),
		[]byte(event),
		isErrorByte,
		isConnectByte,
		isDisconnectByte,
		body,
	}, messageSeparator)
}

func deserializeMessage(decrypt MessageDecrypt, b []byte) Message {
	if decrypt != nil {
		b = decrypt(b)
	}

	namespace, event, body, err, isConnect, isDisconnect, isInvalid := deserializeInput(b)
	return Message{
		namespace,
		event,
		body,
		err,
		err != nil,
		isConnect,
		isDisconnect,
		isInvalid,
		"",
	}
}

func deserializeInput(b []byte) (
	namespace string,
	event string,
	body []byte,
	err error,
	isConnect bool,
	isDisconnect bool,
	isInvalid bool,
) {

	dts := bytes.SplitN(b, messageSeparator, 6)
	if len(dts) != 6 {
		isInvalid = true
		return
	}

	if namespaceB := dts[0]; len(namespaceB) == 0 {
		isInvalid = true
		return
	} else {
		namespace = string(namespaceB)
	}

	event = string(dts[1])
	isError := bytes.Equal(dts[2], trueByte)
	isConnect = bytes.Equal(dts[3], trueByte)
	isDisconnect = bytes.Equal(dts[4], trueByte)
	if b := dts[5]; len(b) > 0 {
		if isError {
			err = errors.New(string(b))
		} else {
			body = b // keep it like that.
		}
	}

	// log.Printf("**message.go:L104 | deserializeInput\nnamespace: %s\nevent: %s\nerr: %v\nisError:%v\nisConnect: %v\nisDisconnect: %v\nbody: %s", namespace, event, err, isError, isConnect, isDisconnect, string(body))
	return
}
