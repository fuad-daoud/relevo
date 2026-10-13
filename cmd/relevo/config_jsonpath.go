package main

import (
	"encoding/json"
	"errors"
)

// jsonAt walks keys into a JSON body, returning the raw value there. A key
// missing, or a body that is not an object at some step, is ok false.
func jsonAt(body []byte, keys []string) (json.RawMessage, bool) {
	cur := json.RawMessage(body)
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// setJSON returns body with value stored at keys, creating intermediate
// objects. A path that runs through a non-object is an error.
func setJSON(body []byte, keys []string, value json.RawMessage) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("not an object")
	}
	if root == nil {
		return nil, errors.New("not an object")
	}
	if err := setAt(root, keys, value); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func setAt(obj map[string]json.RawMessage, keys []string, value json.RawMessage) error {
	key := keys[0]
	if len(keys) == 1 {
		obj[key] = value
		return nil
	}

	child := map[string]json.RawMessage{}
	if existing, ok := obj[key]; ok {
		if err := json.Unmarshal(existing, &child); err != nil || child == nil {
			return errors.New("not an object")
		}
	}
	if err := setAt(child, keys[1:], value); err != nil {
		return err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return err
	}
	obj[key] = encoded
	return nil
}

// unsetJSON returns body with the key at keys removed, and removed false when
// the path is missing. A path that runs through a non-object is an error.
func unsetJSON(body []byte, keys []string) (out []byte, removed bool, err error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, errors.New("not an object")
	}
	if root == nil {
		return nil, false, errors.New("not an object")
	}
	removed, err = delAt(root, keys)
	if err != nil {
		return nil, false, err
	}
	if !removed {
		return nil, false, nil
	}
	out, err = json.Marshal(root)
	return out, true, err
}

func delAt(obj map[string]json.RawMessage, keys []string) (bool, error) {
	key := keys[0]
	if len(keys) == 1 {
		if _, ok := obj[key]; !ok {
			return false, nil
		}
		delete(obj, key)
		return true, nil
	}

	existing, ok := obj[key]
	if !ok {
		return false, nil
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(existing, &child); err != nil || child == nil {
		return false, errors.New("not an object")
	}
	removed, err := delAt(child, keys[1:])
	if err != nil || !removed {
		return false, err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return false, err
	}
	obj[key] = encoded
	return true, nil
}
