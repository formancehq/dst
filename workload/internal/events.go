package internal

import (
	"bytes"
	"encoding/json"
)

const KAFKA_BOOTSTRAP_SERVERS = "formance-kafka-bootstrap.kafka.svc.cluster.local:9092"

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
