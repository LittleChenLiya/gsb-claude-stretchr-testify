package mock

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"
)

// SnapshotFunc produces an independent snapshot of the given value. It is
// used by the mock argument snapshot mode (see [Mock.EnableArgumentSnapshots])
// for types that cannot be snapshotted by the built-in deep-copy logic, such
// as funcs, channels, or structs with unexported fields.
//
// The returned value must be safe to retain: later mutations of the original
// value must not affect it. Returning an error aborts the recording of the
// current call and reports the failure to the test.
type SnapshotFunc func(value interface{}) (interface{}, error)

var snapshotFuncRegistry = struct {
	sync.RWMutex
	funcs map[reflect.Type]SnapshotFunc
}{funcs: make(map[reflect.Type]SnapshotFunc)}

// RegisterSnapshotFunc registers a custom snapshot function for the type of
// the given (non-nil) sample value. The function is used whenever the mock
// argument snapshot mode encounters a value of exactly that type, taking
// precedence over the built-in snapshot logic. This is the supported escape
// hatch for types the built-in logic refuses to copy (funcs, chans, structs
// with unexported fields, unsafe pointers).
//
// Registering a function for a type that already has one replaces it.
// RegisterSnapshotFunc panics if sample or fn is nil.
func RegisterSnapshotFunc(sample interface{}, fn SnapshotFunc) {
	if sample == nil {
		panic("mock: RegisterSnapshotFunc requires a non-nil sample value")
	}
	if fn == nil {
		panic("mock: RegisterSnapshotFunc requires a non-nil snapshot function")
	}
	t := reflect.TypeOf(sample)
	snapshotFuncRegistry.Lock()
	defer snapshotFuncRegistry.Unlock()
	snapshotFuncRegistry.funcs[t] = fn
}

// UnregisterSnapshotFunc removes the custom snapshot function registered for
// the type of the given sample value, if any. It is mainly useful in tests.
func UnregisterSnapshotFunc(sample interface{}) {
	if sample == nil {
		return
	}
	t := reflect.TypeOf(sample)
	snapshotFuncRegistry.Lock()
	defer snapshotFuncRegistry.Unlock()
	delete(snapshotFuncRegistry.funcs, t)
}

func lookupSnapshotFunc(t reflect.Type) (SnapshotFunc, bool) {
	snapshotFuncRegistry.RLock()
	defer snapshotFuncRegistry.RUnlock()
	fn, ok := snapshotFuncRegistry.funcs[t]
	return fn, ok
}

// snapshotVisit identifies a reference that was already copied during one
// snapshot pass, so that shared references stay shared and cycles terminate.
type snapshotVisit struct {
	typ reflect.Type
	ptr unsafe.Pointer
}

// snapshotter deep-copies argument values while preserving the reference
// topology of the original object graph: values that were shared between
// arguments (or within one argument) remain shared in the snapshot, and
// cyclic structures are reproduced as cyclic structures.
type snapshotter struct {
	visits map[snapshotVisit]reflect.Value
}

// snapshotArguments deep-copies every argument exactly once. A single
// snapshotter is used for the whole argument list, so references shared
// across arguments are preserved.
func snapshotArguments(args Arguments) (Arguments, error) {
	s := &snapshotter{visits: make(map[snapshotVisit]reflect.Value)}
	snapped := make(Arguments, len(args))
	for i, arg := range args {
		v, err := s.snapshotValue(reflect.ValueOf(arg))
		if err != nil {
			return nil, fmt.Errorf("mock: cannot snapshot argument %d: %w", i, err)
		}
		if !v.IsValid() {
			snapped[i] = nil
			continue
		}
		snapped[i] = v.Interface()
	}
	return snapped, nil
}

func (s *snapshotter) snapshotValue(v reflect.Value) (reflect.Value, error) {
	if !v.IsValid() {
		return v, nil
	}

	// A registered custom snapshot function always wins, including for
	// kinds the built-in logic does not support.
	if fn, ok := lookupSnapshotFunc(v.Type()); ok && v.CanInterface() {
		out, err := fn(v.Interface())
		if err != nil {
			return reflect.Value{}, fmt.Errorf("custom snapshot function for %s failed: %w", v.Type(), err)
		}
		if out == nil {
			return reflect.Zero(v.Type()), nil
		}
		ov := reflect.ValueOf(out)
		if !ov.Type().AssignableTo(v.Type()) {
			return reflect.Value{}, fmt.Errorf("custom snapshot function for %s returned incompatible type %s", v.Type(), ov.Type())
		}
		return ov, nil
	}

	switch v.Kind() {
	case reflect.Ptr:
		return s.snapshotPtr(v)
	case reflect.Interface:
		return s.snapshotInterface(v)
	case reflect.Struct:
		return s.snapshotStruct(v)
	case reflect.Slice:
		return s.snapshotSlice(v)
	case reflect.Array:
		return s.snapshotArray(v)
	case reflect.Map:
		return s.snapshotMap(v)
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return reflect.Value{}, fmt.Errorf(
			"values of kind %s (type %s) cannot be snapshotted; register a custom snapshot function with mock.RegisterSnapshotFunc",
			v.Kind(), v.Type())
	default:
		// Basic kinds (bool, ints, floats, complex, string) are immutable
		// and can be reused as-is.
		return v, nil
	}
}

func (s *snapshotter) snapshotPtr(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return reflect.Zero(v.Type()), nil
	}
	key := snapshotVisit{typ: v.Type(), ptr: unsafe.Pointer(v.Pointer())}
	if prev, ok := s.visits[key]; ok {
		return prev, nil
	}
	out := reflect.New(v.Type().Elem())
	// Memoize before recursing so that cycles back to this pointer resolve
	// to the copy.
	s.visits[key] = out
	elem, err := s.snapshotValue(v.Elem())
	if err != nil {
		return reflect.Value{}, err
	}
	out.Elem().Set(elem)
	return out, nil
}

func (s *snapshotter) snapshotInterface(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return reflect.Zero(v.Type()), nil
	}
	elem, err := s.snapshotValue(v.Elem())
	if err != nil {
		return reflect.Value{}, err
	}
	out := reflect.New(v.Type()).Elem()
	out.Set(elem)
	return out, nil
}

func (s *snapshotter) snapshotStruct(v reflect.Value) (reflect.Value, error) {
	t := v.Type()
	out := reflect.New(t).Elem()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			// Unexported fields cannot be read without unsafe access.
			// Refuse to guess instead of silently recording a zero value.
			return reflect.Value{}, fmt.Errorf(
				"struct %s has unexported field %q which cannot be snapshotted safely; register a custom snapshot function with mock.RegisterSnapshotFunc",
				t, field.Name)
		}
		fv, err := s.snapshotValue(v.Field(i))
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(fv)
	}
	return out, nil
}

func (s *snapshotter) snapshotSlice(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return reflect.Zero(v.Type()), nil
	}
	out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	if v.Len() > 0 {
		key := snapshotVisit{typ: v.Type(), ptr: unsafe.Pointer(v.Pointer())}
		if prev, ok := s.visits[key]; ok {
			return prev, nil
		}
		s.visits[key] = out
	}
	for i := 0; i < v.Len(); i++ {
		ev, err := s.snapshotValue(v.Index(i))
		if err != nil {
			return reflect.Value{}, err
		}
		out.Index(i).Set(ev)
	}
	return out, nil
}

func (s *snapshotter) snapshotArray(v reflect.Value) (reflect.Value, error) {
	out := reflect.New(v.Type()).Elem()
	for i := 0; i < v.Len(); i++ {
		ev, err := s.snapshotValue(v.Index(i))
		if err != nil {
			return reflect.Value{}, err
		}
		out.Index(i).Set(ev)
	}
	return out, nil
}

func (s *snapshotter) snapshotMap(v reflect.Value) (reflect.Value, error) {
	if v.IsNil() {
		return reflect.Zero(v.Type()), nil
	}
	key := snapshotVisit{typ: v.Type(), ptr: unsafe.Pointer(v.Pointer())}
	if prev, ok := s.visits[key]; ok {
		return prev, nil
	}
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	s.visits[key] = out
	for _, k := range v.MapKeys() {
		sk, err := s.snapshotValue(k)
		if err != nil {
			return reflect.Value{}, err
		}
		sv, err := s.snapshotValue(v.MapIndex(k))
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetMapIndex(sk, sv)
	}
	return out, nil
}
