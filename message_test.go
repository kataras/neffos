package neffos

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestMessageSerialization(t *testing.T) {
	var tests = []struct {
		msg        Message // in
		serialized []byte  // out
	}{
		{ // 0
			msg: Message{
				Namespace: "default",
				Room:      "room1",
				Event:     OnNamespaceConnect,
				wait:      "0",
			},
			serialized: []byte("0;default;room1;_OnNamespaceConnect;0;0;"),
		},
		{ // 1
			msg: Message{
				Namespace: "default",
				Body:      []byte("some id"),
				Event:     OnNamespaceConnect,
			},
			serialized: []byte(";default;;_OnNamespaceConnect;0;0;some id"),
		},
		{ // 2
			msg: Message{
				Namespace: "default",
				Event:     OnNamespaceDisconnect,
			},
			serialized: []byte(";default;;_OnNamespaceDisconnect;0;0;"),
		},
		{ // 3
			msg: Message{
				Namespace: "default",
				Event:     "chat",
				Body:      []byte("text"),
			},
			serialized: []byte(";default;;chat;0;0;text"),
		},
		{ // 4
			msg: Message{
				Namespace: "default",
				Event:     "chat",
				Err:       fmt.Errorf("error message"),
				isError:   true,
			},
			serialized: []byte(";default;;chat;1;0;error message"),
		},
		{ // 5
			msg: Message{
				Namespace: "default",
				Event:     "chat",
				Body:      []byte("a body with many ; delimeters; like that;"),
			},
			serialized: []byte(";default;;chat;0;0;a body with many ; delimeters; like that;"),
		},
		{ // 6
			msg: Message{
				Namespace: "",
				Event:     "chat",
				Err:       fmt.Errorf("an error message with many ; delimeters; like that;"),
				isError:   true,
			},
			serialized: []byte(";;;chat;1;0;an error message with many ; delimeters; like that;"),
		},
		{ // 7
			msg: Message{
				Namespace: "default",
				Event:     "chat",
				Body:      []byte("body"),
				wait:      "1",
				isNoOp:    true,
			},
			serialized: []byte("1;default;;chat;0;1;body"),
		},
	}

	for i, tt := range tests {
		got := serializeMessage(tt.msg)
		if !bytes.Equal(got, tt.serialized) {
			t.Fatalf("[%d] serialize: expected %s but got %s", i, tt.serialized, got)
		}

		msg := DeserializeMessage(TextMessage, got, false, false)
		if !reflect.DeepEqual(msg, tt.msg) {
			t.Fatalf("[%d] deserialize: expected\n%#+v but got\n%#+v", i, tt.msg, msg)
		}
	}

	msg := DeserializeMessage(TextMessage, []byte("default;chat;"), false, false)
	if !msg.isInvalid {
		t.Fatalf("expected message to be invalid but it seems that it is a valid one")
	}

	nativeMessage := []byte("a native websocket message")
	msg = DeserializeMessage(TextMessage, nativeMessage, true, false)
	if msg.isInvalid {
		t.Fatalf("expected message to be valid native/raw websocket messageeven")
	}

	nativeMessage = []byte("0;if;we;have;same;number;of;message;tokens;this should pass")
	msg = DeserializeMessage(TextMessage, nativeMessage, true, true)
	if msg.isInvalid {
		t.Fatalf("expected message to be valid native/raw websocket messageeven")
	}

	expectedNativeMessage := Message{
		Event:    OnNativeMessage,
		Body:     nativeMessage,
		IsNative: true,
	}
	if !reflect.DeepEqual(expectedNativeMessage, msg) {
		t.Fatalf("expected a native message to be:\n%#+v\n\tbut got:\n%#+v", expectedNativeMessage, msg)
	}

	// test escape/unescape.
	msg = Message{
		Namespace: "contains;semi",
		Room:      ";this;for sure;",
		Event:     "thatdoesnot",
	}
	expectedSerialized := []byte(fmt.Sprintf(";contains%ssemi;%sthis%sfor sure%s;thatdoesnot;0;0;",
		messageFieldSeparatorReplacement, messageFieldSeparatorReplacement, messageFieldSeparatorReplacement, messageFieldSeparatorReplacement))

	gotSerialized := serializeMessage(msg)

	if !bytes.Equal(expectedSerialized, gotSerialized) {
		t.Fatalf("expected escaped serialized to be: %s but got: %s", string(expectedSerialized), string(gotSerialized))
	}

	msgGot := DeserializeMessage(TextMessage, gotSerialized, false, false)
	if !reflect.DeepEqual(msg, msgGot) {
		t.Fatalf("expected a unescaped message to be:\n%#+v\n\tbut got:\n%#+v", msg, msgGot)
	}
}
