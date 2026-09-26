package mock

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ErrSnapshotUnsupported is returned (wrapped) when an argument cannot be
// snapshotted because it contains a value that cannot be copied safely, such
// as a func, a channel or a struct field that is not accessible. Register a
// custom snapshot function with [RegisterSnapshotFunc] to support such types.
var ErrSnapshotUnsupported = errors.New("mock: argument cannot be snapshotted")

// SnapshotFunc produces an isolated copy of value. It is used by
// [RegisterSnapshotFunc] to teach the mock package how to snapshot types that
// the default reflection based snapshotter cannot handle (for example types
// with unexported fields).
//
// The returned value must be assignable to the type of value and must not
// share mutable state with it. Returning an error aborts the snapshot; the
// triggering call is then reported as a mock failure and is not recorded.
// Returning (nil, nil) for a non-nil value is treated as an error: snapshot
// functions must never silently turn a value into nil.
type SnapshotFunc func(value interface{}) (interface{}, error)

var snapshotRegistry = struct {
	sync.RWMutex
	funcs map[reflect.Type]SnapshotFunc
}{funcs: make(map[reflect.Type]SnapshotFunc)}

// RegisterSnapshotFunc registers a custom snapshot function for the concrete
// type of sample. The function is consulted before the built-in snapshotter
// whenever a value of exactly that type is snapshotted, including values
// nested inside other arguments. It panics if sample or fn is nil.
//
// Registration is global and safe for concurrent use; tests that register
// snapshot functions should unregister them with [UnregisterSnapshotFunc]
// when done.
func RegisterSnapshotFunc(sample interface{}, fn SnapshotFunc) {
	if sample == nil {
		panic("mock: RegisterSnapshotFunc requires a non-nil sample value")
	}
	if fn == nil {
		panic("mock: RegisterSnapshotFunc requires a non-nil function")
	}
	snapshotRegistry.Lock()
	defer snapshotRegistry.Unlock()
	snapshotRegistry.funcs[reflect.TypeOf(sample)] = fn
}

// UnregisterSnapshotFunc removes the custom snapshot function registered for
// the concrete type of sample, if any.
func UnregisterSnapshotFunc(sample interface{}) {
	if sample == nil {
		return
	}
	snapshotRegistry.Lock()
	defer snapshotRegistry.Unlock()
	delete(snapshotRegistry.funcs, reflect.TypeOf(sample))
}

func lookupSnapshotFunc(t reflect.Type) (SnapshotFunc, bool) {
	snapshotRegistry.RLock()
	defer snapshotRegistry.RUnlock()
	fn, ok := snapshotRegistry.funcs[t]
	return fn, ok
}

// snapshotVisit identifies an already snapshotted reference so that shared
// references stay shared and cycles terminate.
type snapshotVisit struct {
	typ reflect.Type
	ptr uintptr
}

type snapshotter struct {
	memo map[snapshotVisit]reflect.Value
}

// snapshotArguments deep-copies args so that later mutations of the original
// values are not visible in the copy. Exactly one snapshotter is used per
// call, which is what preserves shared references and cycles across all
// arguments of that call.
func snapshotArguments(args Arguments) (Arguments, error) {
	s := &snapshotter{memo: make(map[snapshotVisit]reflect.Value)}
	snapshot := make(Arguments, len(args))
	for i, arg := range args {
		v, err := s.snapshotValue(reflect.ValueOf(arg), fmt.Sprintf("argument %d", i))
		if err != nil {
			return nil, err
		}
		if !v.IsValid() {
			snapshot[i] = nil
			continue
		}
		snapshot[i] = v.Interface()
	}
	return snapshot, nil
}

func isNilable(k reflect.Kind) bool {
	switch k {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
		return true
	}
	return false
}

func (s *snapshotter) snapshotValue(v reflect.Value, path string) (reflect.Value, error) {
	if !v.IsValid() {
		return v, nil
	}
	t := v.Type()

	if fn, ok := lookupSnapshotFunc(t); ok {
		return s.snapshotWithFunc(v, fn, path)
	}

	switch v.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		// Immutable scalar values can be shared with the snapshot.
		return v, nil

	case reflect.Ptr:
		if v.IsNil() {
			return reflect.Zero(t), nil
		}
		visit := snapshotVisit{typ: t, ptr: v.Pointer()}
		if cached, ok := s.memo[visit]; ok {
			return cached, nil
		}
		copy := reflect.New(t.Elem())
		// Memoize before recursing so cycles back to this pointer terminate.
		s.memo[visit] = copy
		elem, err := s.snapshotValue(v.Elem(), path+"(*)")
		if err != nil {
			return reflect.Value{}, err
		}
		copy.Elem().Set(elem)
		return copy, nil

	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(t), nil
		}
		elem, err := s.snapshotValue(v.Elem(), path+"("+t.String()+")")
		if err != nil {
			return reflect.Value{}, err
		}
		copy := reflect.New(t).Elem()
		copy.Set(elem)
		return copy, nil

	case reflect.Struct:
		copy := reflect.New(t).Elem()
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				return reflect.Value{}, fmt.Errorf("%w: unexported field %s of %s at %s is not accessible; register a custom snapshot function with mock.RegisterSnapshotFunc",
					ErrSnapshotUnsupported, field.Name, t, path)
			}
			fieldCopy, err := s.snapshotValue(v.Field(i), path+"."+field.Name)
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Field(i).Set(fieldCopy)
		}
		return copy, nil

	case reflect.Array:
		copy := reflect.New(t).Elem()
		for i := 0; i < v.Len(); i++ {
			elem, err := s.snapshotValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(elem)
		}
		return copy, nil

	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(t), nil
		}
		visit := snapshotVisit{typ: t, ptr: v.Pointer()}
		if cached, ok := s.memo[visit]; ok {
			return cached, nil
		}
		copy := reflect.MakeSlice(t, v.Len(), v.Len())
		s.memo[visit] = copy
		for i := 0; i < v.Len(); i++ {
			elem, err := s.snapshotValue(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.Index(i).Set(elem)
		}
		return copy, nil

	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(t), nil
		}
		visit := snapshotVisit{typ: t, ptr: v.Pointer()}
		if cached, ok := s.memo[visit]; ok {
			return cached, nil
		}
		copy := reflect.MakeMapWithSize(t, v.Len())
		s.memo[visit] = copy
		iter := v.MapRange()
		for iter.Next() {
			key, err := s.snapshotValue(iter.Key(), path+"{key}")
			if err != nil {
				return reflect.Value{}, err
			}
			value, err := s.snapshotValue(iter.Value(), fmt.Sprintf("%s[%v]", path, iter.Key()))
			if err != nil {
				return reflect.Value{}, err
			}
			copy.SetMapIndex(key, value)
		}
		return copy, nil

	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		if v.IsNil() {
			// A nil func, channel or pointer cannot be mutated; keep it as is.
			return reflect.Zero(t), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: %s at %s cannot be copied safely; register a custom snapshot function with mock.RegisterSnapshotFunc",
			ErrSnapshotUnsupported, t, path)
	}

	return reflect.Value{}, fmt.Errorf("%w: %s at %s", ErrSnapshotUnsupported, t, path)
}

func (s *snapshotter) snapshotWithFunc(v reflect.Value, fn SnapshotFunc, path string) (reflect.Value, error) {
	snapshot, err := fn(v.Interface())
	if err != nil {
		return reflect.Value{}, fmt.Errorf("mock: custom snapshot function for %s failed at %s: %w", v.Type(), path, err)
	}
	if snapshot == nil {
		if !isNilable(v.Kind()) || !v.IsNil() {
			return reflect.Value{}, fmt.Errorf("mock: custom snapshot function for %s returned nil for a non-nil value at %s", v.Type(), path)
		}
		return reflect.Zero(v.Type()), nil
	}
	sv := reflect.ValueOf(snapshot)
	if !sv.Type().AssignableTo(v.Type()) {
		return reflect.Value{}, fmt.Errorf("mock: custom snapshot function for %s returned %s at %s, which is not assignable to it", v.Type(), sv.Type(), path)
	}
	return sv, nil
}
