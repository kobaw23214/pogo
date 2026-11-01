package llm

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"sort"
	"strings"
)

func xmlToken(d *xml.Decoder) (xml.Token, error) {
	for {
		t, err := d.Token()
		if err != nil {
			return nil, err
		}
		if c, ok := t.(xml.CharData); ok && strings.TrimSpace(string(c)) == "" {
			continue
		}
		return t, nil
	}
}

func element(t xml.Token, name string) (xml.StartElement, bool) {
	s, ok := t.(xml.StartElement)
	return s, ok && s.Name.Space == "" && s.Name.Local == name
}

func namedElement(s xml.StartElement, parameter bool) (string, error) {
	name, seen := "", map[string]bool{}
	for _, a := range s.Attr {
		if a.Name.Space != "" || seen[a.Name.Local] {
			return "", errors.New("unexpected or duplicate XML attribute")
		}
		seen[a.Name.Local] = true
		switch {
		case a.Name.Local == "name":
			name = a.Value
		case parameter && a.Name.Local == "string" && (a.Value == "true" || a.Value == "false"):
			// A known model dialect. The schema, never this hint, owns the type.
		default:
			return "", errors.New("unexpected XML attribute")
		}
	}
	if name == "" {
		return "", errors.New("missing name")
	}
	return name, nil
}

func parseXMLCalls(body string) ([]textCall, error) {
	if !strings.HasPrefix(strings.TrimSpace(body), "<tool_name>") {
		return parseXMLInvokes(body)
	}
	// This observed dialect carries a JSON object inside parameters, not one
	// parameter element per argument. Decode XML text first, then strict JSON;
	// JSON types and duplicate keys must never be silently changed.
	d := xml.NewDecoder(strings.NewReader(body))
	readText := func(name string) (string, error) {
		t, err := xmlToken(d)
		if err != nil {
			return "", err
		}
		start, ok := element(t, name)
		if !ok || len(start.Attr) != 0 {
			return "", errors.New("unexpected XML element")
		}
		var value strings.Builder
		for {
			t, err = d.Token()
			if err != nil {
				return "", err
			}
			if end, ok := t.(xml.EndElement); ok && end.Name == start.Name {
				return value.String(), nil
			}
			chars, ok := t.(xml.CharData)
			if !ok {
				return "", errors.New("XML element must contain text only")
			}
			value.Write(chars)
		}
	}
	name, err := readText("tool_name")
	if err != nil || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return nil, errors.New("invalid tool_name element")
	}
	parameters, err := readText("parameters")
	if err != nil {
		return nil, err
	}
	value, err := strictJSON([]byte(parameters))
	if err != nil {
		return nil, err
	}
	args, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("parameters must contain a JSON object")
	}
	if _, err := xmlToken(d); err != io.EOF {
		return nil, errors.New("unexpected trailing XML")
	}
	return []textCall{{name: name, args: args}}, nil
}

func parseXMLInvokes(body string) ([]textCall, error) {
	d := xml.NewDecoder(strings.NewReader(body))
	var calls []textCall
	for {
		t, err := xmlToken(d)
		if err == io.EOF {
			return calls, nil
		}
		if err != nil {
			return nil, err
		}
		invoke, ok := element(t, "invoke")
		if !ok || len(calls) >= maxTextToolCalls {
			return nil, errors.New("expected invoke")
		}
		name, err := namedElement(invoke, false)
		if err != nil {
			return nil, err
		}
		args := map[string]string{}
		for {
			t, err = xmlToken(d)
			if err != nil {
				return nil, err
			}
			if end, ok := t.(xml.EndElement); ok && end.Name == invoke.Name {
				break
			}
			param, ok := element(t, "parameter")
			if !ok {
				return nil, errors.New("expected parameter")
			}
			key, err := namedElement(param, true)
			if err != nil {
				return nil, err
			}
			if _, exists := args[key]; exists {
				return nil, errors.New("duplicate parameter")
			}
			var value strings.Builder
			for {
				t, err = d.Token()
				if err != nil {
					return nil, err
				}
				if end, ok := t.(xml.EndElement); ok && end.Name == param.Name {
					break
				}
				c, ok := t.(xml.CharData)
				if !ok {
					return nil, errors.New("parameter must contain text only")
				}
				value.Write(c)
			}
			args[key] = value.String()
		}
		calls = append(calls, textCall{name: name, xml: args})
	}
}

func encodeTextCall(name string, args json.RawMessage, format string) (string, error) {
	if format == "json" {
		payload, err := json.Marshal(struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}{name, args})
		return "<tool_call>" + string(payload) + "</tool_call>", err
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(args, &params); err != nil {
		return "", err
	}
	var b bytes.Buffer
	e := xml.NewEncoder(&b)
	root := xml.StartElement{Name: xml.Name{Local: "tool_call"}}
	invoke := xml.StartElement{Name: xml.Name{Local: "invoke"}, Attr: []xml.Attr{{Name: xml.Name{Local: "name"}, Value: name}}}
	if err := e.EncodeToken(root); err != nil {
		return "", err
	}
	_ = e.EncodeToken(invoke)
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw := params[key]
		text := string(raw)
		if len(raw) > 0 && raw[0] == '"' {
			if err := json.Unmarshal(raw, &text); err != nil {
				return "", err
			}
		}
		param := xml.StartElement{Name: xml.Name{Local: "parameter"}, Attr: []xml.Attr{{Name: xml.Name{Local: "name"}, Value: key}}}
		_ = e.EncodeToken(param)
		_ = e.EncodeToken(xml.CharData(text))
		_ = e.EncodeToken(param.End())
	}
	_ = e.EncodeToken(invoke.End())
	_ = e.EncodeToken(root.End())
	if err := e.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}
