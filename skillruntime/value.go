package skillruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

func strictValue(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON is not valid UTF-8")
	}
	if len(raw) > 1<<20 {
		return nil, errors.New("JSON exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, errors.New("JSON exceeds depth limit")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				value := map[string]any{}
				for d.More() {
					k, e := d.Token()
					if e != nil {
						return nil, e
					}
					key, ok := k.(string)
					if !ok {
						return nil, errors.New("invalid object key")
					}
					if _, exists := value[key]; exists {
						return nil, fmt.Errorf("duplicate JSON key: %s", key)
					}
					item, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					value[key] = item
				}
				_, err = d.Token()
				return value, err
			case '[':
				value := []any{}
				for d.More() {
					item, e := read(depth + 1)
					if e != nil {
						return nil, e
					}
					value = append(value, item)
				}
				_, err = d.Token()
				return value, err
			default:
				return nil, errors.New("invalid JSON delimiter")
			}
		}
		return token, nil
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("trailing JSON data")
	}
	return v, nil
}
