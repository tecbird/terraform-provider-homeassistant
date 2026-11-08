package client

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// RegistryString is a string in a registry payload. Home Assistant stores a registry field as a
// number when the integration reports one — a Broadlink firmware version, a numeric identifier
// component — so decoding accepts a string or a number and keeps the value as a string.
type RegistryString string

// UnmarshalJSON accepts a JSON string or number.
func (s *RegistryString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if string(data) == "null" {
		return fmt.Errorf("registry value is null")
	}
	if len(data) > 0 && data[0] == '"' {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*s = RegistryString(value)
		return nil
	}
	var value json.Number
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("registry value %s is neither a string nor a number", data)
	}
	*s = RegistryString(value.String())
	return nil
}

// StringPointer returns the value as a *string: nil for nil.
func (s *RegistryString) StringPointer() *string {
	if s == nil {
		return nil
	}
	value := string(*s)
	return &value
}

// StringRows returns rows of registry strings (identifiers, connections) as plain strings.
func StringRows(rows [][]RegistryString) [][]string {
	out := make([][]string, len(rows))
	for i, row := range rows {
		values := make([]string, len(row))
		for j, value := range row {
			values[j] = string(value)
		}
		out[i] = values
	}
	return out
}
