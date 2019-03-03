package fastws

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type testValue struct {
	From    string `json:"from" xml:"from"`
	To      string `json:"to" xml:"to"`
	Message string `json:"message" xml:"message"`
}

func TestEncoding(t *testing.T) {
	var (
		expectedFromServer = testValue{
			From:    "server",
			To:      "client",
			Message: "Ping",
		}
	)
	f := New()
	f.OnError = func(c *Conn) bool {
		t.Fatal(c.Err())
		return false // disconnect.
	}
	f.OnConnected = func(c *Conn) error {
		c.SetEncoding(json.NewEncoder(c), json.NewDecoder(c))
		return c.Encode(expectedFromServer)
		// return c.WriteJSON(expectedFromServer)
	}

	srv := http.Server{
		Addr:    "localhost:8080",
		Handler: http.HandlerFunc(f.UpgradeHTTP),
	}
	defer srv.Close()
	go srv.ListenAndServe()
	time.Sleep(200 * time.Millisecond)

	c, err := Dial(nil, "ws://localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	c.SetEncoding(json.NewEncoder(c), json.NewDecoder(c))

	var got testValue
	// err = c.ReadJSON(&got)
	err = c.Decode(&got)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(expectedFromServer, got) {
		t.Fatalf("expected:%#+v but got:\n%#+v", expectedFromServer, got)
	}
}
