package ws

import (
	"bytes"
)

type message struct {
	room  string
	event string
	body  []byte
}

var messageSeparator = []byte{';'}

func serializeOutput(msg message) []byte {
	// TODO: may require some more checks here.
	return bytes.Join([][]byte{
		[]byte(msg.room),
		[]byte(msg.event),
		msg.body,
	}, messageSeparator)
}

func deserializeInput(b []byte) (msg message) {
	dts := bytes.SplitN(b, messageSeparator, 3)
	switch len(dts) {
	case 0:
		msg.body = b
	case 1:
		msg.body = dts[0]
	case 2:
		msg.room = string(dts[0])
		msg.body = dts[1]
	case 3:
		msg.room = string(dts[0])
		msg.event = string(dts[1])
		msg.body = dts[2]
	}

	return
}
