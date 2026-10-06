package neffos

import (
	"bytes"
	"fmt"
	"reflect"
	"sync"
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

func TestGenWaitUnique(t *testing.T) {
	const (
		workers = 8
		perG    = 2000
	)

	results := make(chan string, workers*perG)
	var wg sync.WaitGroup
	for g := range workers {
		wg.Add(1)
		go func(client bool) {
			defer wg.Done()
			for range perG {
				results <- genWait(client)
			}
		}(g%2 == 0)
	}
	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, workers*perG)
	for w := range results {
		if w[0] == waitComesFromClientPrefix {
			w = w[1:]
		}
		if _, dup := seen[w]; dup {
			t.Fatalf("duplicate wait token %q", w)
		}
		seen[w] = struct{}{}
	}

	if w := genWait(true); w[0] != waitComesFromClientPrefix {
		t.Fatalf("expected a client token to start with %q, got %q", waitComesFromClientPrefix, w)
	}
	if w := genWait(false); w[0] == waitComesFromClientPrefix {
		t.Fatalf("expected a server token without the client prefix, got %q", w)
	}
}

func TestGenWaitStackExchangeMarker(t *testing.T) {
	tests := []struct{ in, want string }{
		{"$abc-1", "$!abc-1"},
		{"k2x9-1f-0a1b2c3d", "k!2x9-1f-0a1b2c3d"},
		{"", ""},
	}
	for _, tt := range tests {
		got := genWaitStackExchange(tt.in)
		if got != tt.want {
			t.Fatalf("genWaitStackExchange(%q): expected %q, got %q", tt.in, tt.want, got)
		}
	}

	// the server side strips the marker back to the clean token.
	marked := genWaitStackExchange("k2x9-1f")
	msg := DeserializeMessage(TextMessage, serializeMessage(Message{wait: marked, Event: "e"}), false, false)
	if msg.wait != "k2x9-1f" || !msg.FromStackExchange {
		t.Fatalf("expected the clean token and FromStackExchange, got wait %q FromStackExchange %v", msg.wait, msg.FromStackExchange)
	}
}

func TestDeserializeKeepsMarkerOnClient(t *testing.T) {
	payload := serializeMessage(Message{wait: "k!2x9-1f", Namespace: "default", Event: "e"})

	client := newConn(newFakeSocket(), Namespaces{"default": Events{}})
	msg := client.DeserializeMessage(TextMessage, payload)
	if msg.wait != "k!2x9-1f" {
		t.Fatalf("client: expected the marked token to be kept, got %q", msg.wait)
	}
	if msg.FromStackExchange {
		t.Fatal("client: expected FromStackExchange to stay false")
	}

	// the reply a client writes back carries the marker.
	if got := serializeMessage(msg); !bytes.HasPrefix(got, []byte("k!2x9-1f;")) {
		t.Fatalf("client: expected the reply to echo the marked token, got %q", got)
	}

	server := newConn(newFakeSocket(), Namespaces{"default": Events{}})
	server.server = &Server{}
	msg = server.DeserializeMessage(TextMessage, payload)
	if msg.wait != "k2x9-1f" || !msg.FromStackExchange {
		t.Fatalf("server: expected the stripped token and FromStackExchange, got %q %v", msg.wait, msg.FromStackExchange)
	}
}

func TestMarshalNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Marshal(nil) panicked: %v", r)
		}
	}()

	if got := Marshal(nil); got != nil {
		t.Fatalf("expected Marshal(nil) to return nil, got %q", got)
	}
}

func TestUnmarshalNil(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Unmarshal(nil) panicked: %v", r)
		}
	}()

	msg := Message{Body: []byte(`{}`)}
	if err := msg.Unmarshal(nil); err == nil {
		t.Fatal("expected Unmarshal(nil) to return an error")
	}
}

func TestSerializeWaitAndFromExplicit(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("serializeMessage panicked: %v", r)
		}
	}()

	got := serializeMessage(Message{wait: "$k2x9-1f", FromExplicit: "neffos(0xabc(id0x1))", Event: "e"})
	if want := []byte("$k2x9-1f;;;e;0;0;"); !bytes.Equal(got, want) {
		t.Fatalf("expected the wait token to win: %q, got %q", want, got)
	}

	got = serializeMessage(Message{FromExplicit: "neffos(0xabc(id0x1))", Event: "e"})
	if want := []byte("neffos(0xabc(id0x1));;;e;0;0;"); !bytes.Equal(got, want) {
		t.Fatalf("expected FromExplicit without a wait token: %q, got %q", want, got)
	}
}
