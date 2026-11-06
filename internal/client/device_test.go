package client

import (
	"context"
	"slices"
	"testing"
)

func TestDevicesDecode(t *testing.T) {
	devices := Devices(fakeWS{"config/device_registry/list": fixed(`[
		{"id": "d1", "name": "Moon", "name_by_user": "Luna", "manufacturer": "Acme",
		 "model": "M1", "sw_version": "1.2", "area_id": "garden", "labels": ["managed"],
		 "config_entries": ["E1"], "identifiers": [["moon", "E1"]], "entry_type": "service",
		 "disabled_by": null, "hw_version": null},
		{"id": "d2", "name": null, "name_by_user": null, "manufacturer": null, "model": null,
		 "sw_version": null, "area_id": null, "labels": [], "config_entries": []}
	]`)})

	d, ok, err := devices.Get(context.Background(), "d1")
	if err != nil || !ok {
		t.Fatalf("Get(d1) = %+v, %v, %v", d, ok, err)
	}
	if d.Name == nil || *d.Name != "Moon" || d.NameByUser == nil || *d.NameByUser != "Luna" ||
		d.Manufacturer == nil || *d.Manufacturer != "Acme" || d.Model == nil || *d.Model != "M1" ||
		d.SWVersion == nil || *d.SWVersion != "1.2" || d.AreaID == nil || *d.AreaID != "garden" ||
		len(d.Labels) != 1 || d.Labels[0] != "managed" || len(d.ConfigEntries) != 1 ||
		d.ConfigEntries[0] != "E1" {
		t.Errorf("d1 = %+v", d)
	}

	e, ok, err := devices.Get(context.Background(), "d2")
	if err != nil || !ok {
		t.Fatalf("Get(d2) = %+v, %v, %v", e, ok, err)
	}
	if e.Name != nil || e.NameByUser != nil || e.Manufacturer != nil || e.Model != nil ||
		e.SWVersion != nil || e.AreaID != nil || len(e.Labels) != 0 || len(e.ConfigEntries) != 0 {
		t.Errorf("d2 = %+v", e)
	}
}

func TestDevicesDecodeNumericRegistryValues(t *testing.T) {
	devices := Devices(fakeWS{"config/device_registry/list": fixed(`[
		{"id": "d3", "name": "Printer", "sw_version": 62092, "hw_version": 2,
		 "identifiers": [["broadlink", 1234], ["x", "y"]], "labels": [], "config_entries": []}
	]`)})

	d, ok, err := devices.Get(context.Background(), "d3")
	if err != nil || !ok {
		t.Fatalf("Get(d3) = %+v, %v, %v", d, ok, err)
	}
	if d.SWVersion == nil || *d.SWVersion != "62092" ||
		d.HWVersion == nil || *d.HWVersion != "2" {
		t.Errorf("versions = %v, %v, want 62092, 2", d.SWVersion, d.HWVersion)
	}
	want := [][]RegistryString{{"broadlink", "1234"}, {"x", "y"}}
	if !slices.EqualFunc(d.Identifiers, want, slices.Equal) {
		t.Errorf("identifiers = %v, want %v", d.Identifiers, want)
	}
}
