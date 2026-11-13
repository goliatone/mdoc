package contribution

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// encoding/json otherwise accepts case-insensitive field aliases and replaces
// unpaired UTF-16 escapes. Both would change the caller's retained intent.
func validEscapes(b []byte) bool {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		v, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if v >= 0xdc00 && v <= 0xdfff {
			return false
		}
		if v >= 0xd800 && v <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

var rawMessageType = reflect.TypeFor[json.RawMessage]()
var jsonUnmarshaller = reflect.TypeFor[json.Unmarshaler]()

func exactShape(b []byte, t reflect.Type) bool {
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Pointer {
		if string(b) == "null" {
			return true
		}
		return exactShape(b, t.Elem())
	}
	if t == rawMessageType || reflect.PointerTo(t).Implements(jsonUnmarshaller) {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if json.Unmarshal(b, &values) != nil || values == nil {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for k, v := range values {
			ft, ok := fields[k]
			if !ok || !exactShape(v, ft) {
				return false
			}
		}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return true
		}
		var values []json.RawMessage
		if json.Unmarshal(b, &values) != nil {
			return false
		}
		for _, v := range values {
			if !exactShape(v, t.Elem()) {
				return false
			}
		}
	default:
		if string(b) == "null" {
			return false
		}
	}
	return true
}
