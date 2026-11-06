package client

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrNoEnergyPrefs means Home Assistant has no Energy dashboard preferences stored yet, which is
// the state of an instance whose Energy dashboard was never set up.
var ErrNoEnergyPrefs = errors.New("no energy preferences stored")

// EnergyPrefs are the Energy dashboard preferences, one JSON array per key of `energy/get_prefs`.
// A key Home Assistant does not return (e.g. `device_consumption_water` in old stored data) is
// nil.
type EnergyPrefs struct {
	EnergySources          json.RawMessage `json:"energy_sources"`
	DeviceConsumption      json.RawMessage `json:"device_consumption"`
	DeviceConsumptionWater json.RawMessage `json:"device_consumption_water"`
}

// params returns the preferences as command fields. A nil key is sent as an empty array, so
// saving EnergyPrefs{} resets the preferences.
func (p EnergyPrefs) params() map[string]any {
	orEmpty := func(raw json.RawMessage) json.RawMessage {
		if len(raw) == 0 || string(raw) == "null" {
			return json.RawMessage(`[]`)
		}
		return raw
	}
	return map[string]any{
		"energy_sources":           orEmpty(p.EnergySources),
		"device_consumption":       orEmpty(p.DeviceConsumption),
		"device_consumption_water": orEmpty(p.DeviceConsumptionWater),
	}
}

// Document returns the preferences as one JSON object with all three keys, a missing key as
// an empty array. It is what the stored baseline is taken of.
func (p EnergyPrefs) Document() (json.RawMessage, error) {
	return json.Marshal(p.params())
}

// Energy reads and writes the Energy dashboard preferences over the WebSocket `energy/*` API.
type Energy struct {
	ws WSCommander
}

// NewEnergy returns the Energy preferences API on ws.
func NewEnergy(ws WSCommander) Energy {
	return Energy{ws: ws}
}

// Get returns the stored preferences. It returns ErrNoEnergyPrefs if there are none.
func (e Energy) Get(ctx context.Context) (EnergyPrefs, error) {
	var prefs EnergyPrefs
	err := e.ws.Command(ctx, "energy/get_prefs", nil, &prefs)
	var wsErr *WSError
	if errors.As(err, &wsErr) && wsErr.Code == "not_found" {
		return EnergyPrefs{}, ErrNoEnergyPrefs
	}
	return prefs, err
}

// Save replaces all three keys of the preferences.
func (e Energy) Save(ctx context.Context, prefs EnergyPrefs) error {
	return e.ws.Command(ctx, "energy/save_prefs", prefs.params(), nil)
}
