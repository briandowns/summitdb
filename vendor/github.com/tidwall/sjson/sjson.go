// Package sjson provides setting json values.
package sjson

import (
	jsongo "encoding/json"
	"reflect"
	"strconv"
	"unsafe"

	"github.com/tidwall/gjson"
)

type errorType struct {
	msg string
}

func (err *errorType) Error() string {
	return err.msg
}

type pathResult struct {
	part  string // current key part
	path  string // remaining path
	force bool   // force a string key
	more  bool   // there is more path to parse
}

func parsePath(path string) (pathResult, error) {
	var r pathResult
	if len(path) > 0 && path[0] == ':' {
		r.force = true
		path = path[1:]
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			r.part = path[:i]
			r.path = path[i+1:]
			r.more = true
			return r, nil
		}
		if path[i] == '*' || path[i] == '?' {
			return r, &errorType{"wildcard characters not allowed in path"}
		} else if path[i] == '#' {
			return r, &errorType{"array access character not allowed in path"}
		}
		if path[i] == '\\' {
			// go into escape mode. this is a slower path that
			// strips off the escape character from the part.
			epart := []byte(path[:i])
			i++
			if i < len(path) {
				epart = append(epart, path[i])
				i++
				for ; i < len(path); i++ {
					if path[i] == '\\' {
						i++
						if i < len(path) {
							epart = append(epart, path[i])
						}
						continue
					} else if path[i] == '.' {
						r.part = string(epart)
						r.path = path[i+1:]
						r.more = true
						return r, nil
					} else if path[i] == '*' || path[i] == '?' {
						return r, &errorType{"wildcard characters not allowed in path"}
					} else if path[i] == '#' {
						return r, &errorType{"array access character not allowed in path"}
					}
					epart = append(epart, path[i])
				}
			}
			// append the last part
			r.part = string(epart)
			return r, nil
		}
	}
	r.part = path
	return r, nil
}

// appendStringify makes a json string and appends to buf.
func appendStringify(buf []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > 0x7f || s[i] == '"' {
			b, _ := jsongo.Marshal(s)
			return append(buf, b...)
		}
	}
	buf = append(buf, '"')
	buf = append(buf, s...)
	buf = append(buf, '"')
	return buf
}

// appendBuild builds a json block from a json path.
func appendBuild(buf []byte, array bool, paths []pathResult, raw string, stringify bool) []byte {
	if !array {
		buf = appendStringify(buf, paths[0].part)
		buf = append(buf, ':')
	}
	if len(paths) > 1 {
		n, numeric := atoui(paths[1])
		if numeric {
			buf = append(buf, '[')
			buf = appendRepeat(buf, "null,", n)
			buf = appendBuild(buf, true, paths[1:], raw, stringify)
			buf = append(buf, ']')
		} else {
			buf = append(buf, '{')
			buf = appendBuild(buf, false, paths[1:], raw, stringify)
			buf = append(buf, '}')
		}
	} else {
		if stringify {
			buf = appendStringify(buf, raw)
		} else {
			buf = append(buf, raw...)
		}
	}
	return buf
}

// atoui does a rip conversion of string -> unigned int.
func atoui(r pathResult) (n int, ok bool) {
	if r.force {
		return 0, false
	}
	for i := 0; i < len(r.part); i++ {
		if r.part[i] < '0' || r.part[i] > '9' {
			return 0, false
		}
		n = n*10 + int(r.part[i]-'0')
	}
	return n, true
}

// appendRepeat repeats string "n" times and appends to buf.
func appendRepeat(buf []byte, s string, n int) []byte {
	for i := 0; i < n; i++ {
		buf = append(buf, s...)
	}
	return buf
}

// trim does a rip trim
func trim(s string) string {
	for len(s) > 0 {
		if s[0] <= ' ' {
			s = s[1:]
			continue
		}
		break
	}
	for len(s) > 0 {
		if s[len(s)-1] <= ' ' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// deleteTailItem deletes the previous key or comma.
func deleteTailItem(buf []byte) ([]byte, bool) {
loop:
	for i := len(buf) - 1; i >= 0; i-- {
		// look for either a ',',':','['
		switch buf[i] {
		case '[':
			return buf, true
		case ',':
			return buf[:i], false
		case ':':
			// delete tail string
			i--
			for ; i >= 0; i-- {
				if buf[i] == '"' {
					i--
					for ; i >= 0; i-- {
						if buf[i] == '"' {
							i--
							if i >= 0 && i == '\\' {
								i--
								continue
							}
							for ; i >= 0; i-- {
								// look for either a ',','{'
								switch buf[i] {
								case '{':
									return buf[:i+1], true
								case ',':
									return buf[:i], false
								}
							}
						}
					}
					break
				}
			}
			break loop
		}
	}
	return buf, false
}

var errNoChange = &errorType{"no change"}

func appendRawPaths(buf []byte, jstr string, paths []pathResult, raw string, stringify, del bool) ([]byte, error) {
	var err error
	var res gjson.Result
	var found bool
	if del {
		if paths[0].part == "-1" && !paths[0].force {
			res = gjson.Get(jstr, "#")
			if res.Int() > 0 {
				res = gjson.Get(jstr, strconv.FormatInt(int64(res.Int()-1), 10))
				found = true
			}
		}
	}
	if !found {
		res = gjson.Get(jstr, paths[0].part)
	}
	if res.Index > 0 {
		if len(paths) > 1 {
			buf = append(buf, jstr[:res.Index]...)
			buf, err = appendRawPaths(buf, res.Raw, paths[1:], raw, stringify, del)
			if err != nil {
				return nil, err
			}
			buf = append(buf, jstr[res.Index+len(res.Raw):]...)
			return buf, nil
		}
		buf = append(buf, jstr[:res.Index]...)
		var exidx int // addional forward stripping
		if del {
			var delNextComma bool
			buf, delNextComma = deleteTailItem(buf)
			if delNextComma {
				for i, j := res.Index+len(res.Raw), 0; i < len(jstr); i, j = i+1, j+1 {
					if jstr[i] <= ' ' {
						continue
					}
					if jstr[i] == ',' {
						exidx = j + 1
					}
					break
				}
			}
		} else {
			if stringify {
				buf = appendStringify(buf, raw)
			} else {
				buf = append(buf, raw...)
			}
		}
		buf = append(buf, jstr[res.Index+len(res.Raw)+exidx:]...)
		return buf, nil
	}
	if del {
		return nil, errNoChange
	}
	n, numeric := atoui(paths[0])
	isempty := true
	for i := 0; i < len(jstr); i++ {
		if jstr[i] > ' ' {
			isempty = false
			break
		}
	}
	if isempty {
		if numeric {
			jstr = "[]"
		} else {
			jstr = "{}"
		}
	}
	jsres := gjson.Parse(jstr)
	if jsres.Type != gjson.JSON {
		if numeric {
			jstr = "[]"
		} else {
			jstr = "{}"
		}
		jsres = gjson.Parse(jstr)
	}
	var comma bool
	for i := 1; i < len(jsres.Raw); i++ {
		if jsres.Raw[i] <= ' ' {
			continue
		}
		if jsres.Raw[i] == '}' || jsres.Raw[i] == ']' {
			break
		}
		comma = true
		break
	}
	switch jsres.Raw[0] {
	default:
		return nil, &errorType{"json must be an object or array"}
	case '{':
		buf = append(buf, '{')
		buf = appendBuild(buf, false, paths, raw, stringify)
		if comma {
			buf = append(buf, ',')
		}
		buf = append(buf, jsres.Raw[1:]...)
		return buf, nil
	case '[':
		var appendit bool
		if !numeric {
			if paths[0].part == "-1" && !paths[0].force {
				appendit = true
			} else {
				return nil, &errorType{"cannot set array element for non-numeric key '" + paths[0].part + "'"}
			}
		}
		if appendit {
			njson := trim(jsres.Raw)
			if njson[len(njson)-1] == ']' {
				njson = njson[:len(njson)-1]
			}
			buf = append(buf, njson...)
			if comma {
				buf = append(buf, ',')
			}

			buf = appendBuild(buf, true, paths, raw, stringify)
			buf = append(buf, ']')
			return buf, nil
		}
		buf = append(buf, '[')
		ress := jsres.Array()
		for i := 0; i < len(ress); i++ {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, ress[i].Raw...)
		}
		if len(ress) == 0 {
			buf = appendRepeat(buf, "null,", n-len(ress))
		} else {
			buf = appendRepeat(buf, ",null", n-len(ress))
			if comma {
				buf = append(buf, ',')
			}
		}
		buf = appendBuild(buf, true, paths, raw, stringify)
		buf = append(buf, ']')
		return buf, nil
	}
}

func set(jstr, path, raw string, stringify, del bool) ([]byte, error) {
	// parse the path, make sure that it does not contain invalid characters
	// such as '#', '?', '*'
	if path == "" {
		return nil, &errorType{"path cannot be empty"}
	}
	paths := make([]pathResult, 0, 4)
	r, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	paths = append(paths, r)
	for r.more {
		if r, err = parsePath(r.path); err != nil {
			return nil, err
		}
		paths = append(paths, r)
	}

	njson, err := appendRawPaths(nil, jstr, paths, raw, stringify, del)
	if err != nil {
		return nil, err
	}
	return njson, nil
}

// Set sets a json value for the specified path.
// A path is in dot syntax, such as "name.last" or "age".
// This function expects that the json is well-formed, and does not validate.
// Invalid json will not panic, but it may return back unexpected results.
// An error is returned if the path is not valid.
//
// A path is a series of keys seperated by a dot.
//
//  {
//    "name": {"first": "Tom", "last": "Anderson"},
//    "age":37,
//    "children": ["Sara","Alex","Jack"],
//    "friends": [
//      {"first": "James", "last": "Murphy"},
//      {"first": "Roger", "last": "Craig"}
//    ]
//  }
//  "name.last"          >> "Anderson"
//  "age"                >> 37
//  "children.1"         >> "Alex"
//
func Set(json, path string, value interface{}) (string, error) {
	jsonh := *(*reflect.StringHeader)(unsafe.Pointer(&json))
	jsonbh := reflect.SliceHeader{Data: jsonh.Data, Len: jsonh.Len}
	jsonb := *(*[]byte)(unsafe.Pointer(&jsonbh))
	res, err := SetBytes(jsonb, path, value)
	return string(res), err
}

type dtype struct{}

// Delete deletes a value from json for the specified path.
func Delete(json, path string) (string, error) {
	return Set(json, path, dtype{})
}

// DeleteBytes deletes a value from json for the specified path.
func DeleteBytes(json []byte, path string) ([]byte, error) {
	return SetBytes(json, path, dtype{})
}

// SetRaw sets a raw json value for the specified path. The works the same as
// Set except that the value is set as a raw block of json. This allows for setting
// premarshalled json objects.
func SetRaw(json, path, value string) (string, error) {
	res, err := set(json, path, value, false, false)
	if err == errNoChange {
		return json, nil
	}
	return string(res), err
}

// SetRawBytes sets a raw json value for the specified path.
// If working with bytes, this method preferred over SetRaw(string(data), path, value)
func SetRawBytes(json []byte, path string, value []byte) ([]byte, error) {
	jstr := *(*string)(unsafe.Pointer(&json))
	vstr := *(*string)(unsafe.Pointer(&value))
	res, err := set(jstr, path, vstr, false, false)
	if err == errNoChange {
		return json, nil
	}
	return res, nil
}

// SetBytes sets a json value for the specified path.
// If working with bytes, this method preferred over Set(string(data), path, value)
func SetBytes(json []byte, path string, value interface{}) ([]byte, error) {
	jstr := *(*string)(unsafe.Pointer(&json))
	var res []byte
	var err error
	switch v := value.(type) {
	default:
		b, err := jsongo.Marshal(value)
		if err != nil {
			return nil, err
		}
		raw := *(*string)(unsafe.Pointer(&b))
		res, err = set(jstr, path, raw, false, false)
	case dtype:
		res, err = set(jstr, path, "", false, true)
	case string:
		res, err = set(jstr, path, v, true, false)
	case []byte:
		raw := *(*string)(unsafe.Pointer(&v))
		res, err = set(jstr, path, raw, true, false)
	case bool:
		if v {
			res, err = set(jstr, path, "true", false, false)
		} else {
			res, err = set(jstr, path, "false", false, false)
		}
	case int8:
		res, err = set(jstr, path, strconv.FormatInt(int64(v), 10), false, false)
	case int16:
		res, err = set(jstr, path, strconv.FormatInt(int64(v), 10), false, false)
	case int32:
		res, err = set(jstr, path, strconv.FormatInt(int64(v), 10), false, false)
	case int64:
		res, err = set(jstr, path, strconv.FormatInt(int64(v), 10), false, false)
	case uint8:
		res, err = set(jstr, path, strconv.FormatUint(uint64(v), 10), false, false)
	case uint16:
		res, err = set(jstr, path, strconv.FormatUint(uint64(v), 10), false, false)
	case uint32:
		res, err = set(jstr, path, strconv.FormatUint(uint64(v), 10), false, false)
	case uint64:
		res, err = set(jstr, path, strconv.FormatUint(uint64(v), 10), false, false)
	case float32:
		res, err = set(jstr, path, strconv.FormatFloat(float64(v), 'f', -1, 64), false, false)
	case float64:
		res, err = set(jstr, path, strconv.FormatFloat(float64(v), 'f', -1, 64), false, false)
	}
	if err == errNoChange {
		return json, nil
	}
	return res, err
}