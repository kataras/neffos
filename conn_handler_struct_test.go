package neffos

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

type testStructStatic struct {
	Err error
}

func (s *testStructStatic) Namespace() string {
	return "default"
}

func (s *testStructStatic) OnMyEvent(c *NSConn, msg Message) error {
	return s.Err
}

func TestConnHandlerStructStatic(t *testing.T) {
	// EnableDebug(nil)

	v := new(testStructStatic)
	v.Err = fmt.Errorf("from static")
	s := NewStruct(v)
	nss := s.GetNamespaces()

	if expected, got := v.Namespace(), s.engine.namespace; expected != got {
		t.Fatalf("expected namespace to be: %s but got: %s", expected, got)
	}

	err := nss[s.engine.namespace]["OnMyEvent"](nil, Message{})
	if err != v.Err {
		t.Fatalf("expected output error to be: %v but got: %v", v.Err, err)
	}
}

type testStructDynamic struct {
	Namespace      string
	StaticFieldErr error

	Conn *NSConn
}

func (s *testStructDynamic) OnMyEvent(msg Message) error {
	return fmt.Errorf("%s%v", s.Conn.namespace, s.StaticFieldErr)
}

func (s *testStructDynamic) OnMySecondEvent(msg Message) error {
	return s.StaticFieldErr
}

func TestConnHandlerStructDynamic(t *testing.T) {
	v := &testStructDynamic{
		Namespace:      "default",
		StaticFieldErr: fmt.Errorf("a static field which should be set on each new testStructDynamic"),
	}
	s := NewStruct(v)
	nss := s.GetNamespaces()

	nsConn := &NSConn{namespace: v.Namespace}
	nss[v.Namespace][OnNamespaceConnect](nsConn, Message{Namespace: v.Namespace})

	err := nss[v.Namespace]["OnMyEvent"](nsConn, Message{})
	if expected, got := v.Namespace+v.StaticFieldErr.Error(), err.Error(); expected != got {
		t.Fatalf("expected output error to be: %v but got: %v", expected, got)
	}

	err = nss[v.Namespace]["OnMySecondEvent"](nsConn, Message{})
	if expected, got := v.StaticFieldErr, err; expected != got {
		t.Fatalf("expected output error to be: %v but got: %v", expected, got)
	}
}

type testStructDynamicEmbedded struct {
	*NSConn
}

func (s *testStructDynamicEmbedded) OnMyEvent(msg Message) error {
	return fmt.Errorf("%s", s.namespace)
}

func TestConnHandlerStructDynamicEmbedded(t *testing.T) {
	v := new(testStructDynamicEmbedded)
	s := NewStruct(v).SetNamespace("default")
	nss := s.GetNamespaces()

	nsConn := &NSConn{namespace: "default"}
	nss["default"][OnNamespaceConnect](nsConn, Message{Namespace: "default"})

	err := nss["default"]["OnMyEvent"](nsConn, Message{})
	if err.Error() != "default" {
		t.Fatalf("expected output error to be: %v but got: %v", "default", err)
	}
}

// injectedStore stands in for an application dependency handed to each
// per-connection controller by a typed injector.
type injectedStore struct{ puts int }

type testStructInjected struct {
	Conn  *NSConn
	store *injectedStore
}

func (s *testStructInjected) OnPut(msg Message) error {
	if s.Conn == nil {
		return fmt.Errorf("NSConn field not set")
	}
	s.store.puts++
	return nil
}

func TestStructTypedInjector(t *testing.T) {
	store := &injectedStore{}
	s := NewStruct(&testStructInjected{}).
		SetNamespace("default").
		SetInjector(func(c *NSConn) *testStructInjected {
			return &testStructInjected{store: store}
		})

	events := s.GetNamespaces()["default"]
	nsConn := &NSConn{namespace: "default"}
	if err := events[OnNamespaceConnect](nsConn, Message{}); err != nil {
		t.Fatal(err)
	}
	if err := events["OnPut"](nsConn, Message{}); err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 {
		t.Fatalf("expected the injected dependency to be used, puts=%d", store.puts)
	}
}

func TestNSConnInstance(t *testing.T) {
	s := NewStruct(&testStructInjected{}).SetNamespace("default").
		SetInjector(func(*NSConn) *testStructInjected { return &testStructInjected{store: &injectedStore{}} })
	events := s.GetNamespaces()["default"]

	nsConn := &NSConn{namespace: "default"}
	if _, ok := nsConn.Instance[testStructInjected](); ok {
		t.Fatal("expected no instance before the namespace connect")
	}

	if err := events[OnNamespaceConnect](nsConn, Message{}); err != nil {
		t.Fatal(err)
	}

	inst, ok := nsConn.Instance[testStructInjected]()
	if !ok || inst == nil {
		t.Fatal("expected the per-connection instance after connect")
	}
	if inst.Conn != nsConn {
		t.Fatal("expected the instance's NSConn field to be set")
	}
	if _, ok := nsConn.Instance[testStructStatic](); ok {
		t.Fatal("expected a wrong type to report false")
	}

	// a static controller has no per-connection instance.
	static := NewStruct(&testStructStatic{})
	staticConn := &NSConn{namespace: "default"}
	if cb, ok := static.GetNamespaces()["default"][OnNamespaceConnect]; ok {
		_ = cb(staticConn, Message{})
	}
	if _, ok := staticConn.Instance[testStructStatic](); ok {
		t.Fatal("expected no instance for a static controller")
	}
}

// TestNewStructValueFromReflect is the iris mvc path: the controller is only
// available as a reflect.Value and its fields are bound before registration.
func TestNewStructValueFromReflect(t *testing.T) {
	v := &testStructDynamic{StaticFieldErr: fmt.Errorf("bound")}
	sv := NewStructValue(reflect.ValueOf(v)).SetNamespace("iris")

	h := JoinConnHandlers(Namespaces{"other": Events{}}, sv)
	events := h.GetNamespaces()["iris"]
	if events == nil {
		t.Fatal("expected the struct's events under the namespace set by SetNamespace")
	}

	nsConn := &NSConn{namespace: "iris"}
	if err := events[OnNamespaceConnect](nsConn, Message{}); err != nil {
		t.Fatal(err)
	}
	if err := events["OnMySecondEvent"](nsConn, Message{}); err == nil || err.Error() != "bound" {
		t.Fatalf("expected the static field to be copied into the instance, got %v", err)
	}
}

type testStructUnexported struct {
	Conn *NSConn

	Label string

	mu    sync.Mutex
	count int
}

func (s *testStructUnexported) OnCount(msg Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return fmt.Errorf("%s:%d", s.Label, s.count)
}

func TestStructUnexportedFieldDoesNotPanic(t *testing.T) {
	proto := &testStructUnexported{Label: "static", count: 5}
	events := NewStruct(proto).SetNamespace("default").GetNamespaces()["default"]

	nsConn := &NSConn{namespace: "default"}
	if err := events[OnNamespaceConnect](nsConn, Message{}); err != nil {
		t.Fatal(err)
	}

	// Label (exported, non-zero) is copied; count (unexported) is not.
	if err := events["OnCount"](nsConn, Message{}); err == nil || err.Error() != "static:1" {
		t.Fatalf("expected exported static fields copied and unexported ones skipped, got %v", err)
	}
}

func TestStructSettingsCarried(t *testing.T) {
	s := NewStruct(&testStructStatic{}).SetPingInterval(7).SetMaxMessageSize(9)
	settings := getSettings(s)
	if settings.pingInterval != 7 || settings.maxMessageSize != 9 {
		t.Fatalf("expected Struct settings to be read, got %+v", settings)
	}
	if got := getSettings(NewStructValue(reflect.ValueOf(&testStructStatic{})).SetTimeouts(1, 2)); got.readTimeout != 1 || got.writeTimeout != 2 {
		t.Fatalf("expected StructValue settings to be read, got %+v", got)
	}
}

func BenchmarkStructDynamicEvent(b *testing.B) {
	events := NewStruct(&testStructDynamic{Namespace: "default"}).GetNamespaces()["default"]
	nsConn := &NSConn{namespace: "default"}
	if err := events[OnNamespaceConnect](nsConn, Message{}); err != nil {
		b.Fatal(err)
	}
	cb := events["OnMySecondEvent"]

	b.ReportAllocs()
	for b.Loop() {
		_ = cb(nsConn, Message{})
	}
}
