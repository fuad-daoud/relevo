// Package jsonshape derives the JSON key paths a Go struct encodes to. The
// format numbers in internal/store and internal/mastermind guard the shape those
// paths describe: a new field is a shape change these paths' golden tests catch.
package jsonshape

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"
)

// Keys returns every JSON key path t encodes to, sorted and unique, following
// encoding/json's own field selection: a `json:"-"` field is skipped, a
// tagged name wins over the Go name, an embedded struct with no tag name is
// flattened, and a slice/array/map adds "[]"/"{}" to the path before
// recursing. time.Time and any json.Marshaler are leaves.
func Keys(t reflect.Type) []string {
	set := map[string]struct{}{}
	walk(t, "", set)

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var timeType = reflect.TypeOf(time.Time{})

var marshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

func isLeaf(t reflect.Type) bool {
	return t == timeType || t.Implements(marshalerType)
}

func walk(t reflect.Type, prefix string, set map[string]struct{}) {
	if isLeaf(t) {
		addKey(prefix, set)
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if isLeaf(t) {
		addKey(prefix, set)
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		walkStruct(t, prefix, set)
	case reflect.Slice, reflect.Array:
		walk(t.Elem(), prefix+"[]", set)
	case reflect.Map:
		walk(t.Elem(), prefix+"{}", set)
	default:
		addKey(prefix, set) // strings, numbers, bools, interfaces, funcs: all leaves
	}
}

// walkStruct flattens an anonymous field with no tag name into prefix, as
// encoding/json promotes it, instead of nesting under the field's name.
func walkStruct(t reflect.Type, prefix string, set map[string]struct{}) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue // unexported and not embedded
		}

		name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
		switch {
		case name == "-":
			continue
		case f.Anonymous && name == "":
			walk(f.Type, prefix, set)
		default:
			if name == "" {
				name = f.Name
			}
			walk(f.Type, join(prefix, name), set)
		}
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// addKey records a terminal path; an empty prefix is the type Keys itself was
// called with, which has nothing to record unless it is a struct.
func addKey(prefix string, set map[string]struct{}) {
	if prefix == "" {
		return
	}
	set[prefix] = struct{}{}
}
