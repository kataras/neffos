package ws

import (
	"bytes"
	"errors"
)

// <wait(0-uint64)>;
// <namespace>;
// <event>;
// <isError(0-1)>;
// <isNoOp(0-1)>;
// <body||error_message>
type Message struct {
	Namespace string
	Event     string
	Body      []byte
	Err       error

	// if true then `Err` is filled by the error message and
	// the last segment of incoming/outcoming serialized message is the error message instead of the body.
	isError bool
	isNoOp  bool

	isInvalid bool

	from string // the CONN ID, filled automatically.

	wait string
}

func (m Message) isConnect() bool {
	return m.Event == OnNamespaceConnect
}

func (m Message) isDisconnect() bool {
	return m.Event == OnNamespaceDisconnect
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
	out = serializeOutput(msg.wait, msg.Namespace, msg.Event, msg.Body, msg.Err, msg.isNoOp)

	if encrypt != nil {
		out = encrypt(out)
	}

	return out
}

// <namespace>;<event>;<body>
func serializeOutput(wait string, namespace string,
	event string,
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

	// if wait > 0 {
	// buf := make([]byte, binary.MaxVarintLen64)
	// n := binary.PutUvarint(buf, wait)
	// waitByte = buf[:n]
	// waitByte = []byte(strconv.FormatUint(wait, 10))
	//	waitByte = []byte(strconv.FormatUint(wait), 10))
	// }

	// if len(wait) > 0 {
	// 	waitByte = wait
	// }

	if wait != "" {
		waitByte = []byte(wait)
	}

	msg := bytes.Join([][]byte{
		waitByte,
		[]byte(namespace),
		[]byte(event),
		isErrorByte,
		isNoOpByte,
		body,
	}, messageSeparator)

	// send := make([]byte, hex.EncodedLen(len(msg)))
	// hex.Encode(send, msg)
	return msg

	// buf := make([]byte, base64.StdEncoding.EncodedLen(len(msg)))
	// base64.StdEncoding.Encode(buf, msg)

	//	return buf
}

func deserializeMessage(decrypt MessageDecrypt, b []byte) Message {
	// read := make([]byte, hex.DecodedLen(len(b)))
	// _, err := hex.Decode(read, b)
	// if err != nil {
	// 	panic("original: " + string(b) + " error: " + err.Error())
	// }

	if decrypt != nil {
		b = decrypt(b)
	}

	namespace, event, body, err, isNoOp, isInvalid, wait := deserializeInput(b)
	return Message{
		namespace,
		event,
		body,
		err,
		err != nil,
		isNoOp,
		isInvalid,
		"",
		wait,
	}
}

func deserializeInput(b []byte) (
	namespace string,
	event string,
	body []byte,
	err error,
	isNoOp bool,
	isInvalid bool,
	wait string,
) {

	// base64Text := make([]byte, base64.StdEncoding.DecodedLen(len(b)))

	// n, _ := base64.StdEncoding.Decode(base64Text, b)
	// b = base64Text[:n]

	dts := bytes.SplitN(b, messageSeparator, 6)
	if len(dts) != 6 {
		isInvalid = true
		return
	}

	// namespace can be empty.
	// if namespaceB := dts[0]; len(namespaceB) == 0 {
	// 	isInvalid = true
	// 	return
	// } else {
	// 	namespace = string(namespaceB)
	// }

	// wait, _ = binary.Uvarint(dts[0])
	// wait = binary.LittleEndian.Uint64(dts[0])

	// 	if !bytes.Equal(dts[0], falseByte) {
	// if not zero then try to convert it.
	// wait, _ = strconv.ParseUint(string(dts[0]), 10, 64)
	// }

	// wait, _ = binary.Uvarint(dts[0])

	//	wait = binary.LittleEndian.Uint64(dts[0])

	// if waitStr := string(dts[0]); waitStr == "server" || waitStr == "client" {
	// 	wait = waitStr
	// }

	// n, _ := strconv.ParseInt(string(dts[0]), 10, 32)
	// wait = uint64(n)
	wait = string(dts[0])
	namespace = string(dts[1])
	event = string(dts[2])
	isError := bytes.Equal(dts[3], trueByte)
	isNoOp = bytes.Equal(dts[4], trueByte)
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
