package neffos

import (
	"reflect"
	"strings"
	"time"
)

// ConnHandler is the interface which
// `Events`, `Namespaces` and `WithTimeout` are implement.
// It's exported just to be used on the `Dial`(client) and `New` (server) functions.
type ConnHandler interface {
	getNamespaces() Namespaces
}

var (
	_ ConnHandler = (Events)(nil)
	_ ConnHandler = (Namespaces)(nil)
	_ ConnHandler = WithTimeout{}
	_ ConnHandler = (*Struct)(nil)
)

// Events completes the `ConnHandler` interface.
// It is a map which its key is the event name
// and its value the event's callback.
//
// Events type completes the `ConnHandler` itself therefore,
// can be used as standalone value on the `New` and `Dial` functions
// to register events on empty namespace as well.
//
// See `Namespaces`, `New` and `Dial` too.
type Events map[string]MessageHandlerFunc

func (e Events) getNamespaces() Namespaces {
	return Namespaces{"": e}
}

func (e Events) fireEvent(c *NSConn, msg Message) error {
	if h, ok := e[msg.Event]; ok {
		return h(c, msg)
	}

	if h, ok := e[OnAnyEvent]; ok {
		return h(c, msg)
	}

	return nil
}

// Namespaces completes the `ConnHandler` interface.
// Can be used to register one or more namespaces on the `New` and `Dial` functions.
// The key is the namespace literal and the value is the `Events`,
// a map with event names and their callbacks.
//
// See `WithTimeout`, `New` and `Dial` too.
type Namespaces map[string]Events

func (nss Namespaces) getNamespaces() Namespaces { return nss }

// WithTimeout completes the `ConnHandler` interface.
// Can be used to register namespaces and events or just events on an empty namespace
// with Read and Write timeouts.
//
// See `New` and `Dial`.
type WithTimeout struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	Namespaces Namespaces
	Events     Events
}

func (t WithTimeout) getNamespaces() Namespaces {
	return JoinConnHandlers(t.Namespaces, t.Events).getNamespaces()
}

func getTimeouts(h ConnHandler) (readTimeout time.Duration, writeTimeout time.Duration) {
	if t, ok := h.(WithTimeout); ok {
		readTimeout = t.ReadTimeout
		writeTimeout = t.WriteTimeout
	}

	return
}

// EventMatcherFunc is a type of which a Struct matches the methods with neffos events.
type EventMatcherFunc = func(methodName string) (string, bool)

// Struct is a ConnHandler. All fields are unexported, use `NewStruct` instead.
// It converts any pointer to a struct value to `neffos.Namespaces` using reflection.
type Struct struct {
	ptr interface{}

	// defaults to empty and tries to get it through `Struct.Namespace() string` method.
	namespace string
	// defaults to nil, if specified
	// then it matches the events based on the result string or false if this method shouldn't register as event.
	eventMatcher EventMatcherFunc

	// if the Ptr contais a NSConnn field then a new Ptr should be created on each new client connection.
	isDynamic         bool
	dynamicFieldIndex int // the NSConn, it should be an exported field one.
	// if isDynamic but contains fields other than NSConn type and they are filled, they are stored here
	// so each new Ptr sets them too. Non-exported are acceptable too. As a special case though
	// if the Ptr is something like type myNSConn neffos.NSConn then the dynamicFieldIndex will be -1.
	staticFields map[int]reflect.Value
	namespaces   Namespaces
}

// SetNamespace sets a namespace that this Struct is responsible for,
// Alterinatively create a method on the controller named `Namespace() string`
// to retrieve this namespace at build time.
func (s *Struct) SetNamespace(namespace string) *Struct {
	s.namespace = namespace
	return s
}

// SetEventMatcher sets an event method matcher.
func (s *Struct) SetEventMatcher(matcher EventMatcherFunc) *Struct {
	s.eventMatcher = matcher
	return s
}

var (
	// EventPrefixMatcher matches methods to events based on the "prefix".
	EventPrefixMatcher = func(prefix string) EventMatcherFunc {
		return func(methodName string) (string, bool) {
			if strings.HasPrefix(methodName, prefix) {
				return methodName, true
			}

			return "", false
		}
	}

	// EventTrimPrefixMatcher matches methods based on the "prefixToTrim"
	// and events are registered without this prefix.
	EventTrimPrefixMatcher = func(prefixToTrim string) EventMatcherFunc {
		return func(methodName string) (string, bool) {
			if strings.HasPrefix(methodName, prefixToTrim) {
				return methodName[len(prefixToTrim):], true
			}

			return "", false
		}
	}
)

// NewStruct returns a new Struct value instance type of ConnHandler.
// The "ptr" should be a pointer to a struct.
// This function is used when you want to convert a structure to
// neffos.ConnHandler based on the struct's methods.
// The methods if "ptr" structure value
// can be func(msg neffos.Message) error if the structure contains a *neffos.NSConn field,
// otherwise they should be like any event callback: func(nsConn *neffos.NSConn, msg neffos.Message) error.
// If contains a field of type *neffos.NSConn then on each event callback a new controller is created
// and static fields(if any) are set on runtime with the NSConn itself.
// If it's a static controller (does not contain a NSConn field)
// then it just registers its functions as regular events without performance cost.
//
// Users of this method is `New` and `Dial`.
//
// Note that this method has a performance cost when an event's callback's logic has small footprint.
func NewStruct(ptr interface{}) *Struct {
	return &Struct{
		ptr: ptr,
	}
}

var (
	nsConnType = reflect.TypeOf((*NSConn)(nil))
	msgType    = reflect.TypeOf(Message{})
	errType    = reflect.TypeOf((*error)(nil)).Elem()
)

func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Struct:
		zero := true
		for i := 0; i < v.NumField(); i++ {
			zero = zero && isZero(v.Field(i))
		}

		if typ := v.Type(); typ != nil && v.IsValid() {
			f, ok := typ.MethodByName("IsZero")
			// if not found
			// if has input arguments (1 is for the value receiver, so > 1 for the actual input args)
			// if output argument is not boolean
			// then skip this IsZero user-defined function.
			if !ok || f.Type.NumIn() > 1 || f.Type.NumOut() != 1 && f.Type.Out(0).Kind() != reflect.Bool {
				return zero
			}

			method := v.Method(f.Index)
			// no needed check but:
			if method.IsValid() && !method.IsNil() {
				// it shouldn't panic here.
				zero = method.Call(nil)[0].Interface().(bool)
			}
		}

		return zero
	case reflect.Func, reflect.Map, reflect.Slice:
		return v.IsNil()
	case reflect.Array:
		zero := true
		for i := 0; i < v.Len(); i++ {
			zero = zero && isZero(v.Index(i))
		}
		return zero
	}
	// if not any special type then use the reflect's .Zero
	// usually for fields, but remember if it's boolean and it's false
	// then it's zero, even if set-ed.

	if !v.CanInterface() {
		// if can't interface, i.e return value from unexported field or method then return false
		return false
	}

	zero := reflect.Zero(v.Type())
	return v.Interface() == zero.Interface()
}

func (s *Struct) getNamespaces() Namespaces {
	if s.namespaces != nil {
		return s.namespaces
	}

	typ := reflect.TypeOf(s.ptr) // use for methods with receiver Ptr.
	if typ.Kind() != reflect.Ptr {
		panic("Struct: Ptr should be a pointer to a struct value")
	}

	elemTyp := typ.Elem() // use for fields.

	if typ.ConvertibleTo(nsConnType) {
		panic("Struct: conversion for type" + typ.String() + " NSConn is not allowed.")
	}

	if elemTyp.Kind() != reflect.Struct {
		panic("Struct: Ptr does not points to a struct")
	}

	v := reflect.ValueOf(s.ptr) // use for methods with receiver Ptr.
	if !v.IsValid() {
		panic("Struct: Ptr is not a valid value")
	}

	elem := v.Elem() // use for fields.

	n := typ.NumMethod()
	if n == 0 || n == 1 && s.namespace == "" {
		panic("Struct: Ptr does not contain any exported method")
	}

	if !s.isDynamic {
		for nFields, i := elemTyp.NumField(), 0; i < nFields; i++ {
			f := elemTyp.Field(i)

			if f.Type == nsConnType && f.PkgPath == "" {
				if f.Anonymous && f.Type.Name() == nsConnType.Name() {
					// It's embedded NSConn, add a warning or let it go?
				}
				s.isDynamic = true
				s.dynamicFieldIndex = i
				continue
			}

			// set static fields that are not zero.
			if fVal := elem.Field(i); !isZero(fVal) {
				if s.staticFields == nil {
					s.staticFields = make(map[int]reflect.Value)
				}

				// println("add " + f.Name + " to the static filled fields")
				s.staticFields[i] = fVal
			}
		}
	}

	events := make(Events, 0)

	// Create the dynamic type which methods will be compared to.
	// remember, the receiver Ptr is also part of the input arguments,
	// that's why we don't use a static type assertion.
	expectedIn := []reflect.Type{
		typ,
		nsConnType,
		msgType,
	}

	if s.isDynamic {
		// Except when the Ptr is a dynamic one (has a field of NSConn) then the event callback does not require
		// that on its input arguments.
		expectedIn = append(expectedIn[0:1], expectedIn[2:]...)
	}

	var msgHandlerType = reflect.FuncOf(expectedIn, []reflect.Type{errType}, false)

	for i := 0; i < n; i++ {
		method := typ.Method(i)

		// If no namespace provided by the Struct and this Ptr's method is a Namespace() string then set the namespace to its out result.
		// Empty namespace is allowed but if more than one Struct with empty namespace
		// is used then the JoinConnHandlers should be used to wrap all them to one namespace as expected.
		if s.namespace == "" && method.Name == "Namespace" && method.Type.NumOut() == 1 && method.Type.Out(0).Kind() == reflect.String {
			s.namespace = v.Method(i).Call(nil)[0].Interface().(string)
			continue
		}

		// Convert each method to MessageHandlerFunc.
		if method.Type != msgHandlerType {
			continue
		}

		eventName := method.Name
		if s.eventMatcher != nil {
			newName, ok := s.eventMatcher(method.Name)
			if !ok {
				continue
			}

			eventName = newName
		}

		// println("found an event callback: "+method.Name+" ("+eventName+") on position: ", i)

		if !s.isDynamic {
			events[eventName] = v.Method(i).Interface().(func(*NSConn, Message) error)
			continue
		}

		events[eventName] = func(c *NSConn, msg Message) error {
			newPtr := reflect.New(elemTyp)
			newPtrElem := newPtr.Elem()

			// don't support type customType neffos.NSConn:
			// newPtrElem.Set(reflect.ValueOf(c).Elem().Convert(elemTyp))

			newPtrElem.Field(s.dynamicFieldIndex).Set(reflect.ValueOf(c))
			if s.staticFields != nil {
				for findex, fvalue := range s.staticFields {
					newPtrElem.Field(findex).Set(fvalue)
				}
			}

			return newPtr.MethodByName(method.Name).Interface().(func(Message) error)(msg)
		}
	}

	s.namespaces = Namespaces{s.namespace: events}

	return s.namespaces
}

// JoinConnHandlers combines two or more "connHandlers"
// and returns a result of a single `ConnHandler` that
// can be passed on the `New` and `Dial` functions.
func JoinConnHandlers(connHandlers ...ConnHandler) ConnHandler {
	namespaces := Namespaces{}

	for _, h := range connHandlers {
		nss := h.getNamespaces()
		if len(nss) > 0 {
			for namespace, events := range nss {
				if events == nil {
					continue
				}
				clonedEvents := make(Events, len(events))
				for evt, cb := range events {
					clonedEvents[evt] = cb
				}

				if curEvents, exists := namespaces[namespace]; exists {
					// fill missing events.
					for evt, cb := range clonedEvents {
						curEvents[evt] = cb
					}

				} else {
					namespaces[namespace] = clonedEvents
				}
			}
		}
	}

	return namespaces
}
