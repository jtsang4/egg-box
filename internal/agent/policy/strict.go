package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	maxDepth = 48
	maxNodes = 4_000_000
)

// decodeStrict parses data into generic values, rejecting what a lenient parser
// would silently accept: duplicate object keys (two parsers could disagree on
// which one wins), trailing data, and absurd nesting. The policy inspects the
// result and the agent runs a re-serialisation of that same tree, so what was
// checked is exactly what runs.
func decodeStrict(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	budget := maxNodes
	v, err := parseValue(dec, 0, &budget)
	if err != nil {
		return nil, err
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("the configuration must be a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the configuration object")
	}
	return obj, nil
}

func parseValue(dec *json.Decoder, depth int, budget *int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("nesting deeper than %d levels", maxDepth)
	}
	if *budget--; *budget < 0 {
		return nil, errors.New("configuration is too large")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			if _, dup := obj[key]; dup {
				return nil, fmt.Errorf("duplicate key %q", key)
			}
			val, err := parseValue(dec, depth+1, budget)
			if err != nil {
				return nil, err
			}
			obj[key] = val
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return obj, nil
	case '[':
		arr := []any{}
		for dec.More() {
			val, err := parseValue(dec, depth+1, budget)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return arr, nil
	default:
		return nil, fmt.Errorf("unexpected delimiter %q", delim)
	}
}

func marshalCanonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
