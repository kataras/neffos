package neffos

import (
	"errors"
	"reflect"
	"strings"
)

func indirectType(typ reflect.Type) reflect.Type {
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	return typ
}

// isZero reports whether v is the zero value of its type: false, 0, "", a nil
// func, map or slice, and an array or struct whose elements are all zero. A
// struct with an `IsZero() bool` method answers for itself.
func isZero(v reflect.Value) bool {
	if v.Kind() == reflect.Struct && v.CanInterface() {
		if z, ok := v.Interface().(interface{ IsZero() bool }); ok {
			return z.IsZero()
		}
	}

	return v.IsZero()
}

// visitFields calls visitor for each top-level field of typ and returns the
// index of the first field for which it returned true, or -1.
// It does not support child elements on purpose.
func visitFields(typ reflect.Type, visitor func(f reflect.StructField) bool) int {
	typ = indirectType(typ)

	for i := range typ.NumField() {
		if visitor(typ.Field(i)) {
			return i
		}
	}

	return -1
}

// getNonZeroFields returns the exported, non-zero fields of the struct v points
// to, by field index. Unexported fields are skipped: they cannot be set on the
// per-connection instance (use an injector for those).
func getNonZeroFields(v reflect.Value) (fields map[int]reflect.Value) {
	v = reflect.Indirect(v)

	visitFields(v.Type(), func(f reflect.StructField) bool {
		if !f.IsExported() {
			return false
		}

		fieldIndex := f.Index[0]
		fieldValue := v.Field(fieldIndex)
		if !isZero(fieldValue) {
			if fields == nil {
				fields = make(map[int]reflect.Value)
			}

			fields[fieldIndex] = fieldValue
		}

		return false
	})

	return
}

func getFieldIndex(forType reflect.Type, fieldType reflect.Type) int {
	return visitFields(forType, func(f reflect.StructField) bool {
		return f.Type == fieldType
	})
}

func resolveStructNamespace(v reflect.Value) (string, bool) {
	// By Namespace() string method.
	typ := v.Type()
	method, ok := typ.MethodByName("Namespace")
	if ok {
		if getNamespace, ok := v.Method(method.Index).Interface().(func() string); ok {
			namespace := getNamespace()
			Debugf("Set namespace [\"%s\"] from method [%s.%s]", func() dargs {
				return dargs{namespace, nameOf(typ), method.Name}
			})

			return namespace, true
		}
	}

	// By field Namespace string with filled value.
	typ = indirectType(typ)
	v = reflect.Indirect(v)
	if f, ok := typ.FieldByNameFunc(func(s string) bool { return s == "Namespace" }); ok {
		if f.Type.Kind() == reflect.String {
			namespace := v.Field(f.Index[0]).String()
			Debugf("Set namespace [\"%s\"] from field [%s.%s]", func() dargs {
				return dargs{namespace, nameOf(typ), f.Name}
			})
			return namespace, true
		}
	}

	return "", false
}

var (
	nsConnType = reflect.TypeFor[*NSConn]()
	msgType    = reflect.TypeFor[Message]()
	errType    = reflect.TypeFor[error]()
)

func makeMessageHandlerFuncType(forType reflect.Type, nsConnFieldIndex int) reflect.Type {
	// Create the dynamic type which methods will be compared to.
	// remember, the receiver Ptr is also part of the input arguments,
	// that's why we don't use a static type assertion.
	expectedIn := []reflect.Type{
		forType,
		nsConnType,
		msgType,
	}

	if nsConnFieldIndex >= 0 {
		// Except when the Ptr is a dynamic one (has a field of NSConn) then the event callback does not require
		// that on its input arguments.
		expectedIn = append(expectedIn[0:1], expectedIn[2:]...)
	}

	return reflect.FuncOf(expectedIn, []reflect.Type{errType}, false)
}

func isArgOf(fnType reflect.Type, argType reflect.Type) bool {
	if fnType.Kind() != reflect.Func {
		panic("isArgOf used on a non-method type")
	}

	for in := range fnType.Ins() {
		if in == argType {
			return true
		}
	}

	return false
}

func makeEventFromMethod(v reflect.Value, method reflect.Method, eventMatcher EventMatcherFunc) (eventName string, cb MessageHandlerFunc) {
	eventName = method.Name

	// if method looks like a system event, i.e
	// OnNamespaceConnected, then convert its registered event name
	// _OnNamespaceConnected which is the correct.
	// We could accept a func like:
	// func(s *myConn) _OnNamespaceConnected(msg neffos.Message) error
	// but Go linting does not allow this and
	// we don't want our users to have yellow boxes everywhere in their editors.
	if IsSystemEvent("_" + eventName) {
		eventName = "_" + eventName
	}

	if !IsSystemEvent(eventName) {
		if eventMatcher != nil {
			newName, ok := eventMatcher(method.Name)
			if !ok {
				return "", nil
			}

			eventName = newName
		}
	}

	if isArgOf(method.Type, nsConnType) {
		// it should accept NSConn - static "controller".
		cb = v.Method(method.Index).Interface().(func(*NSConn, Message) error)
	} else {
		// the NSConn exists on the "controller" itself which is set dynamically;
		// the method values are bound once per connection, see makeEventsFromStruct.
		idx := method.Index
		cb = func(c *NSConn, msg Message) error {
			inst, ok := c.value.(*structInstance)
			if !ok {
				return errStructNotConnected
			}

			return inst.methods[idx](msg)
		}
	}

	return
}

// errStructNotConnected is returned by a dynamic struct event that fires on a
// namespace connection that never ran its OnNamespaceConnect.
var errStructNotConnected = errors.New("neffos: struct handler: namespace not connected")

// structInstance is the per-connection instance of a dynamic struct handler.
type structInstance struct {
	// ptr is the *T created by the injector (or reflect.New by default).
	ptr any
	// methods holds the event methods bound to ptr, by reflect method index.
	methods []func(Message) error
}

func nameOf(structType reflect.Type) string {
	structType = indirectType(structType)

	pkg := structType.PkgPath()
	if _, last, ok := strings.CutLast(pkg, "/"); ok {
		pkg = last
	}

	return pkg + "." + structType.Name()
}

func makeEventsFromStruct(v reflect.Value, eventMatcher EventMatcherFunc, injector func(*NSConn) reflect.Value) Events {
	events := make(Events)

	typ := v.Type()

	// get the index of field of a "NSConn" type.
	nsConnFieldIndex := getFieldIndex(typ, nsConnType)
	msgHandlerType := makeMessageHandlerFuncType(typ, nsConnFieldIndex)

	// the method indices of the dynamic events, bound per connection below.
	var dynamicMethods []int

	for method := range typ.Methods() {
		if method.Type != msgHandlerType {
			continue
		}

		eventName, cb := makeEventFromMethod(v, method, eventMatcher)
		if cb == nil {
			continue
		}

		if !isArgOf(method.Type, nsConnType) {
			dynamicMethods = append(dynamicMethods, method.Index)
		}

		Debugf("Event [\"%s\"] is handled by [%s.%s] method", func() dargs {
			return dargs{eventName, nameOf(typ), method.Name}
		})

		events[eventName] = cb
	}

	if nsConnFieldIndex != -1 {
		numMethods := typ.NumMethod()
		typ = indirectType(typ)

		var staticFields map[int]reflect.Value

		if injector == nil {
			// maybe this should be added no matter what, I have to check
			// some things in our company's production server first.
			staticFields = getNonZeroFields(v)

			debugEach(staticFields, func(idx int, f reflect.Value) {
				fval := f.Interface()
				fname := typ.Field(idx).Name
				if fname == "Namespace" {
					// let's no log this as user field because
					// it's optionally used to provide a namespace on NewStruct.GetNamespaces().
					return
				}

				Debugf("Field [%s.%s] marked as static on value [%v]", nameOf(typ), fname, fval)
			})

			injector = func(*NSConn) reflect.Value {
				return reflect.New(typ)
			}
		}

		cb, hasNamespaceConnect := events[OnNamespaceConnect]

		events[OnNamespaceConnect] = func(c *NSConn, msg Message) error {
			cachePtr := injector(c)
			cacheElem := cachePtr.Elem()

			// set the NSConn dynamic field.
			cacheElem.Field(nsConnFieldIndex).Set(reflect.ValueOf(c))

			// set any static fields if default injector (see above).
			for findex, fvalue := range staticFields {
				cacheElem.Field(findex).Set(fvalue)
			}

			// Bind the event methods once, so each event is a plain call.
			inst := &structInstance{
				ptr:     cachePtr.Interface(),
				methods: make([]func(Message) error, numMethods),
			}
			for _, idx := range dynamicMethods {
				inst.methods[idx] = cachePtr.Method(idx).Interface().(func(Message) error)
			}

			// Store it for the rest of the events inside
			// this namespace of that specific connection.
			c.value = inst

			if hasNamespaceConnect {
				return cb(c, msg)
			}

			return nil
		}
	}

	return events
}
