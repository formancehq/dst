package internal

import (
	"bytes"
	"encoding/json"
)

func ExtractEventPayload[T any](p any, validate func(T) error) (*T, error) {
	jsonstr, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var payload T
	dec := json.NewDecoder(bytes.NewReader(jsonstr))
	dec.UseNumber()
	err = dec.Decode(&payload)
	if err != nil {
		return nil, err
	}
	if err := validate(payload); err != nil {
		return nil, err
	}
	return &payload, nil
}
