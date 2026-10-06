package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kataras/neffos"
	"github.com/kataras/neffos/gorilla"
)

// startServers starts servers a and b on one bus and returns their URLs.
func startServers(t *testing.T) (a, b *neffos.Server, urlA, urlB string) {
	t.Helper()

	shared := newBus()
	a, b = newServer("a", shared), newServer("b", shared)
	for _, srv := range []*neffos.Server{a, b} {
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		t.Cleanup(srv.Close)
		url := "ws" + strings.TrimPrefix(ts.URL, "http")
		if srv == a {
			urlA = url
		} else {
			urlB = url
		}
	}
	return a, b, urlA, urlB
}

// connect dials url as name with events and enters "chat".
func connect(t *testing.T, url, name string, events neffos.Namespaces) *neffos.NSConn {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, url+"?name="+name, events)
	if err != nil {
		t.Fatalf("%s: dial: %v", name, err)
	}
	t.Cleanup(client.Close)

	c, err := client.Connect(ctx, "chat")
	if err != nil {
		t.Fatalf("%s: connect: %v", name, err)
	}
	return c
}

func TestChatAcrossServers(t *testing.T) {
	_, _, urlA, urlB := startServers(t)

	got := make(chan string, 1)
	connect(t, urlA, "alice", neffos.Namespaces{"chat": neffos.Events{
		"Chat": func(c *neffos.NSConn, msg neffos.Message) error {
			got <- string(msg.Body)
			return nil
		},
	}})
	bob := connect(t, urlB, "bob", clientEvents)

	bob.Emit("Chat", []byte("hello"))
	select {
	case body := <-got:
		if body != "bob: hello" {
			t.Fatalf("alice: got %q, want %q", body, "bob: hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alice on server a got nothing from bob on server b")
	}
}

func TestAskAcrossServers(t *testing.T) {
	a, _, _, urlB := startServers(t)
	connect(t, urlB, "bob", clientEvents)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Server a asks bob, whose connection lives on server b.
	reply, err := a.Ask(ctx, neffos.Message{To: "bob", Namespace: "chat", Event: "Ping"})
	if err != nil {
		t.Fatalf("ask bob: %v", err)
	}
	if string(reply.Body) != "pong from bob" {
		t.Fatalf("ask bob: got %q, want %q", reply.Body, "pong from bob")
	}

	// Nobody called carol: the context ends the wait.
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := a.Ask(ctx, neffos.Message{To: "carol", Namespace: "chat", Event: "Ping"}); err != context.DeadlineExceeded {
		t.Fatalf("ask carol: expected context.DeadlineExceeded, got %v", err)
	}
}
