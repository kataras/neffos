package neffos

import (
	"maps"
	"reflect"
	"strings"
	"time"
)

// ConnHandler is the interface which namespaces and events can be retrieved through.
// Built-in ConnHandlers are the `Events`, `Namespaces`, `WithTimeout` and `Struct` (see `NewStruct`).
// Users of this are the `Dial`(client) and `New` (server) functions.
type ConnHandler interface {
	GetNamespaces() Namespaces
}

var (
	_ ConnHandler = (Events)(nil)
	_ ConnHandler = (Namespaces)(nil)
	_ ConnHandler = WithTimeout{}
	_ ConnHandler = (*Struct[struct{}])(nil)
	_ ConnHandler = (*StructValue)(nil)
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

// GetNamespaces returns an empty namespace with the "e" Events.
func (e Events) GetNamespaces() Namespaces {
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

// On is a shortcut of Events { eventName: msgHandler }.
// It registers a callback "msgHandler" for an event "eventName".
func (e Events) On(eventName string, msgHandler MessageHandlerFunc) {
	e[eventName] = msgHandler
}

// Namespaces completes the `ConnHandler` interface.
// Can be used to register one or more namespaces on the `New` and `Dial` functions.
// The key is the namespace literal and the value is the `Events`,
// a map with event names and their callbacks.
//
// See `WithTimeout`, `New` and `Dial` too.
type Namespaces map[string]Events

// GetNamespaces just returns the "nss" namespaces.
func (nss Namespaces) GetNamespaces() Namespaces { return nss }

// On is a shortcut of Namespaces { namespace: Events: { eventName: msgHandler } }.
// It registers a callback "msgHandler" for an event "eventName" of the particular "namespace".
func (nss Namespaces) On(namespace, eventName string, msgHandler MessageHandlerFunc) Events {
	if nss[namespace] == nil {
		nss[namespace] = make(Events)
	}
	nss[namespace][eventName] = msgHandler

	return nss[namespace]
}

// WithTimeout completes the `ConnHandler` interface.
// Can be used to register namespaces and events or just events on an empty namespace
// with Read and Write timeouts, a heartbeat and a message size cap.
//
// See `New` and `Dial`.
type WithTimeout struct {
	ReadTimeout  time.Duration
	WriteTimeout time.Duration

	// PingInterval, when above zero, makes the connection ping the remote
	// side every PingInterval and close if no pong arrives within the same
	// interval. It needs a Socket that implements SocketPinger; with any
	// other Socket it does nothing. Pongs are handled on the reader
	// goroutine, so an event callback that runs longer than PingInterval
	// delays them and the heartbeat then closes the connection with a timeout.
	PingInterval time.Duration
	// MaxMessageSize, when above zero, caps the size in bytes of one
	// incoming message. A bigger message closes the connection with
	// CloseMessageTooBig, and Conn.Err reports ErrMessageTooBig. It needs a
	// Socket that implements SocketReadLimiter; with any other Socket it
	// does nothing.
	MaxMessageSize int64

	Namespaces Namespaces
	Events     Events
}

// GetNamespaces returns combined namespaces from "Namespaces" and "Events" fields
// with read and write timeouts from "ReadTimeout" and "WriteTimeout" fields of "t".
func (t WithTimeout) GetNamespaces() Namespaces {
	return JoinConnHandlers(t.Namespaces, t.Events).GetNamespaces()
}

// connSettings are the per-connection settings a ConnHandler can carry.
type connSettings struct {
	readTimeout, writeTimeout time.Duration
	pingInterval              time.Duration
	maxMessageSize            int64
}

func (s connSettings) isZero() bool {
	return s == connSettings{}
}

// merge returns s with every non-zero field of other copied over it.
func (s connSettings) merge(other connSettings) connSettings {
	if other.readTimeout != 0 {
		s.readTimeout = other.readTimeout
	}
	if other.writeTimeout != 0 {
		s.writeTimeout = other.writeTimeout
	}
	if other.pingInterval != 0 {
		s.pingInterval = other.pingInterval
	}
	if other.maxMessageSize != 0 {
		s.maxMessageSize = other.maxMessageSize
	}
	return s
}

// settingsCarrier is implemented by the ConnHandlers that carry per-connection
// settings: WithTimeout, *StructValue and *Struct[T].
type settingsCarrier interface {
	settings() connSettings
}

func (t WithTimeout) settings() connSettings {
	return connSettings{
		readTimeout:    t.ReadTimeout,
		writeTimeout:   t.WriteTimeout,
		pingInterval:   t.PingInterval,
		maxMessageSize: t.MaxMessageSize,
	}
}

// getSettings returns the settings carried by h, or zero settings for a
// handler without any.
func getSettings(h ConnHandler) connSettings {
	if c, ok := h.(settingsCarrier); ok {
		return c.settings()
	}

	return connSettings{}
}

// EventMatcherFunc is a type of which a Struct matches the methods with neffos events.
type EventMatcherFunc = func(methodName string) (string, bool)

// EventPrefixMatcher matches methods to events based on the "prefix".
func EventPrefixMatcher(prefix string) EventMatcherFunc {
	return func(methodName string) (string, bool) {
		if strings.HasPrefix(methodName, prefix) {
			return methodName, true
		}

		return "", false
	}
}

// EventTrimPrefixMatcher matches methods based on the "prefixToTrim"
// and events are registered without this prefix.
func EventTrimPrefixMatcher(prefixToTrim string) EventMatcherFunc {
	return func(methodName string) (string, bool) {
		if strings.HasPrefix(methodName, prefixToTrim) {
			return methodName[len(prefixToTrim):], true
		}

		return "", false
	}
}

// StructValue is the reflection-driven ConnHandler behind `Struct`. It is for
// frameworks that only have the controller as a `reflect.Value` (iris mvc builds
// its controllers through a dependency injection container). Application code
// uses `NewStruct`, which is typed. All fields are unexported, use `NewStructValue`.
type StructValue struct {
	ptr reflect.Value

	// defaults to empty and tries to get it through `Struct.Namespace() string` method.
	namespace string
	// defaults to nil, if specified
	// then it matches the events based on the result string or false if this method shouldn't register as event.
	eventMatcher              EventMatcherFunc
	readTimeout, writeTimeout time.Duration
	pingInterval              time.Duration
	maxMessageSize            int64

	// This field is set when external dependency injection system is used.
	injector func(nsConn *NSConn) reflect.Value

	events Events
}

// NewStructValue is `NewStruct` for a controller held as a `reflect.Value`.
// The value must be a non-nil pointer to a struct with at least one exported
// method; anything else panics, as a programming error at registration time.
func NewStructValue(v reflect.Value) *StructValue {
	if !v.IsValid() {
		panic("NewStruct: value is not a valid one")
	}

	typ := v.Type() // use for methods with receiver Ptr.

	if typ.Kind() != reflect.Pointer {
		panic("NewStruct: value should be a pointer")
	}

	if v.IsNil() {
		panic("NewStruct: value is nil")
	}

	if typ.ConvertibleTo(nsConnType) {
		panic("NewStruct: conversion for type" + typ.String() + " NSConn is not allowed.")
	}

	if indirectType(typ).Kind() != reflect.Struct {
		panic("NewStruct: value does not points to a struct")
	}

	n := typ.NumMethod()
	_, hasNamespaceMethod := typ.MethodByName("Namespace")
	if n == 0 || (n == 1 && hasNamespaceMethod) {
		panic("NewStruct: value does not contain any exported methods")
	}

	return &StructValue{ptr: v}
}

// SetNamespace sets the namespace this handler is responsible for.
// See `Struct.SetNamespace`.
func (s *StructValue) SetNamespace(namespace string) *StructValue {
	s.namespace = namespace
	return s
}

// SetEventMatcher sets the method-to-event matcher. See `Struct.SetEventMatcher`.
func (s *StructValue) SetEventMatcher(matcher EventMatcherFunc) *StructValue {
	s.eventMatcher = matcher
	return s
}

// SetTimeouts sets the read and write deadlines. See `Struct.SetTimeouts`.
func (s *StructValue) SetTimeouts(read, write time.Duration) *StructValue {
	s.readTimeout = read
	s.writeTimeout = write
	return s
}

// SetPingInterval sets the heartbeat interval. See `Struct.SetPingInterval`.
func (s *StructValue) SetPingInterval(d time.Duration) *StructValue {
	s.pingInterval = d
	return s
}

// SetMaxMessageSize caps the size of one incoming message. See `Struct.SetMaxMessageSize`.
func (s *StructValue) SetMaxMessageSize(n int64) *StructValue {
	s.maxMessageSize = n
	return s
}

// SetInjector sets the function that builds the per-connection controller
// instance when the struct has a `*NSConn` field. The returned value must be
// a pointer to the same struct type; neffos sets its `*NSConn` field afterwards.
// Static fields are not copied when an injector is set. See `Struct.SetInjector`.
func (s *StructValue) SetInjector(fn func(nsConn *NSConn) reflect.Value) *StructValue {
	s.injector = fn
	return s
}

func (s *StructValue) settings() connSettings {
	return connSettings{
		readTimeout:    s.readTimeout,
		writeTimeout:   s.writeTimeout,
		pingInterval:   s.pingInterval,
		maxMessageSize: s.maxMessageSize,
	}
}

// Events builds and returns the Events. See `Struct.Events`.
func (s *StructValue) Events() Events {
	if s.events != nil {
		return s.events
	}

	s.events = makeEventsFromStruct(s.ptr, s.eventMatcher, s.injector)
	return s.events
}

// GetNamespaces completes the `ConnHandler` interface. See `Struct.GetNamespaces`.
func (s *StructValue) GetNamespaces() Namespaces {
	if s.namespace == "" {
		s.namespace, _ = resolveStructNamespace(s.ptr)
	}

	return Namespaces{
		s.namespace: s.Events(),
	}
}

// Struct is a ConnHandler built from the exported methods of a struct of type T.
// All fields are unexported, use `NewStruct` instead.
//
// A method named `OnChat` with the signature of a `MessageHandlerFunc`,
// `func(c *neffos.NSConn, msg neffos.Message) error`, handles the "OnChat" event
// (see `SetEventMatcher` to change the mapping). System events are matched by
// name without the leading underscore: `OnNamespaceConnected` handles
// `_OnNamespaceConnected`.
//
// When T has a field of type `*neffos.NSConn` the struct is dynamic: a new T is
// created for every connection to the namespace (see `SetInjector`), the field is
// set to that connection, and the methods take the shorter
// `func(msg neffos.Message) error` form. Exported fields that are non-zero on the
// prototype passed to `NewStruct` are copied into each instance; unexported fields
// are not. Without such a field the struct is static and its methods are
// registered as plain events with no per-connection cost.
type Struct[T any] struct {
	engine *StructValue
}

// NewStruct returns a ConnHandler built from the exported methods of *T.
// "prototype" is the value whose exported, non-zero fields are copied into every
// per-connection instance of a dynamic struct; for a static struct it is the
// receiver of every event. A nil prototype, a non-struct T, or a T without
// exported methods panics, as a programming error at registration time.
//
//	type chat struct {
//		Conn *neffos.NSConn
//		Users *userStore
//	}
//
//	func (c *chat) OnChat(msg neffos.Message) error { ... }
//
//	server := neffos.New(upgrader, neffos.NewStruct(&chat{Users: store}).SetNamespace("default"))
//
// Users of this handler are `New` and `Dial`.
func NewStruct[T any](prototype *T) *Struct[T] {
	if prototype == nil {
		panic("NewStruct: value is nil")
	}

	return &Struct[T]{engine: NewStructValue(reflect.ValueOf(prototype))}
}

// SetNamespace sets a namespace that this Struct is responsible for,
// Alterinatively create a method on the controller named `Namespace() string`
// to retrieve this namespace at build time.
func (s *Struct[T]) SetNamespace(namespace string) *Struct[T] {
	s.engine.SetNamespace(namespace)
	return s
}

// SetEventMatcher sets an event method matcher which applies to every
// event except the system events (OnNamespaceConnected, and so on).
// See `EventPrefixMatcher` and `EventTrimPrefixMatcher`.
func (s *Struct[T]) SetEventMatcher(matcher EventMatcherFunc) *Struct[T] {
	s.engine.SetEventMatcher(matcher)
	return s
}

// SetTimeouts sets read and write deadlines on the underlying network connection.
// After a read or write have timed out, the websocket connection is closed.
//
// Defaults to 0, no timeout except an `Upgrader` or `Dialer` specifies its own values.
func (s *Struct[T]) SetTimeouts(read, write time.Duration) *Struct[T] {
	s.engine.SetTimeouts(read, write)
	return s
}

// SetPingInterval sets the heartbeat interval. Above zero, the connection
// pings the remote side every d and closes if no pong arrives within d.
// It needs a Socket that implements SocketPinger.
// See `WithTimeout.PingInterval`.
//
// Defaults to 0, no heartbeat.
func (s *Struct[T]) SetPingInterval(d time.Duration) *Struct[T] {
	s.engine.SetPingInterval(d)
	return s
}

// SetMaxMessageSize caps the size in bytes of one incoming message. Above
// zero, a bigger message closes the connection with CloseMessageTooBig. It
// needs a Socket that implements SocketReadLimiter.
// See `WithTimeout.MaxMessageSize`.
//
// Defaults to 0, no cap except the one of the `Upgrader` or `Dialer`.
func (s *Struct[T]) SetMaxMessageSize(n int64) *Struct[T] {
	s.engine.SetMaxMessageSize(n)
	return s
}

// SetInjector sets the function that builds the per-connection instance of a
// dynamic struct (one with a `*NSConn` field). It is called once per connection
// to the namespace; neffos sets the `*NSConn` field on the returned value, so
// the function only fills the application's own dependencies:
//
//	neffos.NewStruct(&chat{}).SetInjector(func(c *neffos.NSConn) *chat {
//		return &chat{Users: store, Log: logger.With("conn", c.Conn.ID())}
//	})
//
// With an injector set, the prototype's fields are not copied.
// Static structs never call it.
func (s *Struct[T]) SetInjector(fn func(nsConn *NSConn) *T) *Struct[T] {
	s.engine.SetInjector(func(c *NSConn) reflect.Value {
		return reflect.ValueOf(fn(c))
	})
	return s
}

func (s *Struct[T]) settings() connSettings {
	return s.engine.settings()
}

// Events builds and returns the Events.
// Callers of this method is users that want to add Structs to different namespaces
// in the same application.
// When a single namespace is used then this call is unnecessary,
// the `Struct` is already a fully featured `ConnHandler` by itself.
func (s *Struct[T]) Events() Events {
	return s.engine.Events()
}

// GetNamespaces creates and returns Namespaces based on the
// pointer to struct value provided by the "s".
func (s *Struct[T]) GetNamespaces() Namespaces { // completes the `ConnHandler` interface.
	return s.engine.GetNamespaces()
}

// JoinConnHandlers combines two or more "connHandlers"
// and returns a result of a single `ConnHandler` that
// can be passed on the `New` and `Dial` functions.
//
// Later handlers override earlier ones on event-name collisions within the
// same namespace.
//
// The settings of `WithTimeout` and `Struct` inputs (timeouts, PingInterval,
// MaxMessageSize) are kept: the result is then a `WithTimeout`, and a later
// non-zero setting overrides an earlier one. Without any setting the result
// is a `Namespaces`.
func JoinConnHandlers(connHandlers ...ConnHandler) ConnHandler {
	namespaces := Namespaces{}
	var settings connSettings

	for _, h := range connHandlers {
		settings = settings.merge(getSettings(h))

		for namespace, events := range h.GetNamespaces() {
			if events == nil {
				continue
			}

			if curEvents, exists := namespaces[namespace]; exists {
				maps.Copy(curEvents, events)
			} else {
				namespaces[namespace] = maps.Clone(events)
			}
		}
	}

	if settings.isZero() {
		return namespaces
	}

	return WithTimeout{
		ReadTimeout:    settings.readTimeout,
		WriteTimeout:   settings.writeTimeout,
		PingInterval:   settings.pingInterval,
		MaxMessageSize: settings.maxMessageSize,
		Namespaces:     namespaces,
	}
}
