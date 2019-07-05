package neffos

import (
	"fmt"
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
	v := new(testStructStatic)
	v.Err = fmt.Errorf("from static")
	s := NewStruct(v)
	s.getNamespaces()

	if expected, got := v.Namespace(), s.namespace; expected != got {
		t.Fatalf("expected namespace to be: %s but got: %s", expected, got)
	}

	err := s.namespaces[s.namespace]["OnMyEvent"](nil, Message{})
	if err != v.Err {
		t.Fatalf("expected output error to be: %v but got: %v", v.Err, err)
	}
}

type testStructDynamic struct {
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
	v := new(testStructDynamic)
	v.StaticFieldErr = fmt.Errorf("a static field which should be set on each new testStructDynamic")
	s := NewStruct(v)
	s.namespace = "default"
	s.getNamespaces()

	nsConn := &NSConn{namespace: s.namespace}
	s.namespaces[s.namespace][OnNamespaceConnected](nsConn, Message{Namespace: s.namespace})

	err := s.namespaces[s.namespace]["OnMyEvent"](nsConn, Message{})
	if expected, got := s.namespace+v.StaticFieldErr.Error(), err.Error(); expected != got {
		t.Fatalf("expected output error to be: %v but got: %v", expected, got)
	}

	err = s.namespaces[s.namespace]["OnMySecondEvent"](nsConn, Message{})
	if expected, got := v.StaticFieldErr, err; expected != got {
		t.Fatalf("expected output error to be: %v but got: %v", expected, got)
	}
}

type testStructDynamicEmbedded struct {
	*NSConn
}

func (s *testStructDynamicEmbedded) OnMyEvent(msg Message) error {
	return fmt.Errorf(s.namespace)
}

func TestConnHandlerStructDynamicEmbedded(t *testing.T) {
	v := new(testStructDynamicEmbedded)
	s := NewStruct(v)
	s.namespace = "default"
	s.getNamespaces()

	nsConn := &NSConn{namespace: s.namespace}
	s.namespaces[s.namespace][OnNamespaceConnected](nsConn, Message{Namespace: s.namespace})

	err := s.namespaces[s.namespace]["OnMyEvent"](nsConn, Message{})
	if err.Error() != s.namespace {
		t.Fatalf("expected output error to be: %v but got: %v", s.namespace, err)
	}
}

// This is dissalowed, although I made it work:
// type testStructNSConn NSConn
//
// func (s *testStructNSConn) OnMyEvent(msg Message) error {

// 	return fmt.Errorf(s.namespace)
// }

// func TestConnHandlerStructNSConnType(t *testing.T) {
// 	var expectedNamespace = "default"

// 	v := new(testStructNSConn)
// 	s := NewStruct(v)
// 	s.namespace = expectedNamespace
// 	s.getNamespaces()

// 	err := s.namespaces[s.namespace]["OnMyEvent"](&NSConn{namespace: expectedNamespace}, Message{})
// 	if err.Error() != expectedNamespace {
// 		t.Fatalf("expected output error to be: %v but got: %v", expectedNamespace, err)
// 	}
// }
