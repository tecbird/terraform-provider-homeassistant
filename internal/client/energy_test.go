package client

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestEnergyGet(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{
		{result: `{"energy_sources":[{"type":"solar","stat_energy_from":"sensor.pv"}],"device_consumption":[{"stat_consumption":"sensor.oven"}],"device_consumption_water":[]}`},
		{result: `{"energy_sources":[]}`},
		{err: &WSError{Code: "not_found", Message: "No prefs"}},
		{err: &WSError{Code: "unknown_error", Message: "boom"}},
	}}
	e := NewEnergy(f)

	got, err := e.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].typ != "energy/get_prefs" {
		t.Errorf("command = %q", f.calls[0].typ)
	}
	if string(got.EnergySources) != `[{"type":"solar","stat_energy_from":"sensor.pv"}]` ||
		string(got.DeviceConsumption) != `[{"stat_consumption":"sensor.oven"}]` ||
		string(got.DeviceConsumptionWater) != `[]` {
		t.Errorf("Get = %s, %s, %s", got.EnergySources, got.DeviceConsumption, got.DeviceConsumptionWater)
	}

	// A key HA does not return is nil, and reads as an empty list in the document.
	got, err = e.Get(t.Context())
	if err != nil || got.DeviceConsumptionWater != nil {
		t.Fatalf("Get without water = %+v, %v", got, err)
	}
	doc, err := got.Document()
	if err != nil || string(doc) != `{"device_consumption":[],"device_consumption_water":[],"energy_sources":[]}` {
		t.Errorf("Document = %s, %v", doc, err)
	}

	if _, err := e.Get(t.Context()); !errors.Is(err, ErrNoEnergyPrefs) {
		t.Errorf("Get on not_found: err = %v, want ErrNoEnergyPrefs", err)
	}
	if _, err := e.Get(t.Context()); err == nil || errors.Is(err, ErrNoEnergyPrefs) {
		t.Errorf("Get on other error: err = %v, want it passed through", err)
	}
}

func TestEnergySave(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{{}, {}}}
	e := NewEnergy(f)

	err := e.Save(t.Context(), EnergyPrefs{
		EnergySources:     json.RawMessage(`[{"type":"solar"}]`),
		DeviceConsumption: json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.calls[0].typ != "energy/save_prefs" {
		t.Errorf("command = %q", f.calls[0].typ)
	}
	// All three keys are sent, so a save replaces the whole document; null and missing are [].
	b, _ := json.Marshal(f.calls[0].params)
	if want := `{"device_consumption":[],"device_consumption_water":[],"energy_sources":[{"type":"solar"}]}`; string(b) != want {
		t.Errorf("params = %s, want %s", b, want)
	}

	// The empty preferences reset everything.
	if err := e.Save(t.Context(), EnergyPrefs{}); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(f.calls[1].params)
	if want := `{"device_consumption":[],"device_consumption_water":[],"energy_sources":[]}`; string(b) != want {
		t.Errorf("reset params = %s, want %s", b, want)
	}
	if reflect.DeepEqual(f.calls[0].params, f.calls[1].params) {
		t.Error("save and reset sent the same params")
	}
}
