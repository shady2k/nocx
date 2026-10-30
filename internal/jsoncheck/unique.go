// Package jsoncheck checks JSON properties encoding/json deliberately accepts.
package jsoncheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// UniqueKeys validates one JSON value and rejects duplicate object keys,
// including names made equivalent by Unicode escapes. It is shared by backup
// imports and authority-bearing runtime messages before typed decoding.
func UniqueKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkObjectDuplicates(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("trailing content after JSON")
}

func checkObjectDuplicates(dec *json.Decoder, depth int) error {
	// Match encoding/json's nesting bound before recursing through tokens.
	if depth > 10000 {
		return errors.New("JSON nesting exceeds decoder limit")
	}
	t, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("expected string key, got %T", keyTok)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := checkObjectDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return fmt.Errorf("expected object end, got %v", end)
		}
	case '[':
		for dec.More() {
			if err := checkObjectDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return fmt.Errorf("expected array end, got %v", end)
		}
	}
	return nil
}
