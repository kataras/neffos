package neffos

import (
	"reflect"
)

func indirectType(typ reflect.Type) reflect.Type {
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}

	return typ
}

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

// does not support child elements on purpose.
func visitFields(typ reflect.Type, visitor func(f reflect.StructField) bool) int {
	typ = indirectType(typ)

	for n, i := typ.NumField(), 0; i < n; i++ {
		f := typ.Field(i)
		found := visitor(f)
		if found {
			return i
		}
	}

	return -1
}

func getNonZeroFields(v reflect.Value) (fields map[int]reflect.Value) {
	v = reflect.Indirect(v)

	visitFields(v.Type(), func(f reflect.StructField) bool {
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
			debugf("set namespace [%s] from method [%s.%s]", func() dargs {
				return dargs{namespace, indirectType(typ).Name(), method.Name}
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
			debugf("set namespace [%s] from field [%s.%s]", func() dargs {
				return dargs{namespace, typ.Name(), f.Name}
			})
			return namespace, true
		}
	}

	return "", false
}

var (
	nsConnType = reflect.TypeOf((*NSConn)(nil))
	msgType    = reflect.TypeOf(Message{})
	errType    = reflect.TypeOf((*error)(nil)).Elem()
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

	for i, n := 0, fnType.NumIn(); i < n; i++ {
		if fnType.In(i) == argType {
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
		// the NSConn exists on the "controller" itself which is set dynamically.
		cb = func(c *NSConn, msg Message) error {
			// load an existing instance which contains the same "c".
			cachePtr := c.Value.Load().(reflect.Value)
			return cachePtr.Method(method.Index).Interface().(func(Message) error)(msg)
		}
	}

	return
}

func makeEventsFromStruct(v reflect.Value, eventMatcher EventMatcherFunc) Events {
	events := make(Events, 0)

	typ := v.Type()

	// get the index of field of a "NSConn" type.
	nsConnFieldIndex := getFieldIndex(typ, nsConnType)
	msgHandlerType := makeMessageHandlerFuncType(typ, nsConnFieldIndex)

	for i, n := 0, typ.NumMethod(); i < n; i++ {
		method := typ.Method(i)

		if method.Type != msgHandlerType {
			continue
		}

		eventName, cb := makeEventFromMethod(v, method, eventMatcher)
		if cb == nil {
			continue
		}

		debugf("event [%s] is handled by [%s.%s]", func() dargs {
			return dargs{eventName, indirectType(typ).Name(), method.Name}
		})

		events[eventName] = cb
	}

	if nsConnFieldIndex != -1 {
		// if it's a dynamic.
		typ = indirectType(typ)

		cb, hasNamespaceConnect := events[OnNamespaceConnect]
		staticFields := getNonZeroFields(v)

		// debugf("field [%s.%s] will be automatically re-filled with [%T(%s)]", func(fidx int, f reflect.Value) dargs {
		// 	fval := f.Interface()
		// 	return dargs{typ.Name(), typ.Field(fidx).Name, fval, fval}
		// }).forEach(staticFields)

		debugEach(staticFields, func(idx int, f reflect.Value) {
			fval := f.Interface()
			fname := typ.Field(idx).Name
			if fname == "Namespace" {
				return // let's no log this as user field because
				// it's optionally used to provide a namespace on NewStruct.getNamespaces().
			}

			debugf("field [%s.%s] marked as static on value [%v]", typ.Name(), fname, fval)
		})

		// if debugEnabled() {
		// 	for fidx, f := range staticFields {
		// 		fval := f.Interface()
		// 		debugf(, typ.Name(), typ.Field(fidx).Name, fval, fval)
		// 	}
		// }

		events[OnNamespaceConnect] = func(c *NSConn, msg Message) error {
			// create a new elem instance.
			cachePtr := reflect.New(typ)
			cacheElem := cachePtr.Elem()

			// set the NSConn dynamic field.
			cacheElem.Field(nsConnFieldIndex).Set(reflect.ValueOf(c))
			if staticFields != nil {
				// set any static fields.
				for findex, fvalue := range staticFields {
					cacheElem.Field(findex).Set(fvalue)
				}
			}

			// Store it for the rest of the events inside
			// this namespace of that specific connection.
			c.Value.Store(cachePtr)

			if hasNamespaceConnect {
				return cb(c, msg)
			}

			return nil
		}
	}

	return events
}
