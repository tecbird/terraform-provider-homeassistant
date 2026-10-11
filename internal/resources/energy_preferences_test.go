package resources

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// fakeEnergy is a WSCommander serving the Energy preferences. Saving merges the keys like HA
// does, then adds rewrite to every energy source, like HA filling in a default on save.
type fakeEnergy struct {
	// stored is the document `energy/get_prefs` returns; nil means none are stored.
	stored map[string]json.RawMessage
	// rewrite, if set, is added to each object of `energy_sources` on save.
	rewrite map[string]any
	calls   []string
}

func (f *fakeEnergy) Command(_ context.Context, typ string, params map[string]any, result any) error {
	f.calls = append(f.calls, typ)
	switch typ {
	case "energy/get_prefs":
		if f.stored == nil {
			return &client.WSError{Code: "not_found", Message: "No prefs"}
		}
		b, _ := json.Marshal(f.stored)
		return json.Unmarshal(b, result)
	case "energy/save_prefs":
		if f.stored == nil {
			f.stored = map[string]json.RawMessage{}
		}
		for k, v := range params {
			b, _ := json.Marshal(v)
			f.stored[k] = b
		}
		if f.rewrite != nil {
			var sources []map[string]any
			_ = json.Unmarshal(f.stored["energy_sources"], &sources)
			for _, s := range sources {
				for k, v := range f.rewrite {
					s[k] = v
				}
			}
			f.stored["energy_sources"], _ = json.Marshal(sources)
		}
		return nil
	}
	return &client.WSError{Code: "unknown_command", Message: typ}
}

const (
	energyUserSources = `[{"type":"solar","stat_energy_from":"sensor.pv_energy"}]`
	energyHASources   = `[{"type":"solar","stat_energy_from":"sensor.pv_energy","filled_in_by_ha":"default"}]`
)

func energyPlan(t *testing.T) energyPreferencesModel {
	t.Helper()
	return energyPreferencesModel{
		ID:                     types.StringUnknown(),
		EnergySources:          mustDyn(t, energyUserSources),
		DeviceConsumption:      mustDyn(t, `[{"stat_consumption":"sensor.oven_energy"}]`),
		DeviceConsumptionWater: mustDyn(t, `[]`),
	}
}

// energyJSON returns the three attributes of m as one JSON object.
func energyJSON(t *testing.T, m energyPreferencesModel) string {
	t.Helper()
	p, err := m.prefs()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := p.Document()
	if err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

func TestEnergyPreferencesPrefsRoundTrip(t *testing.T) {
	plan := energyPlan(t)
	prefs, err := plan.prefs()
	if err != nil {
		t.Fatal(err)
	}
	back, err := energyPreferencesFrom(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := energyJSON(t, back), energyJSON(t, plan); got != want {
		t.Errorf("round trip = %s, want %s", got, want)
	}
	if back.ID.ValueString() != "energy_preferences" {
		t.Errorf("id = %q", back.ID.ValueString())
	}

	// A key HA does not return reads as an empty list.
	partial, err := energyPreferencesFrom(client.EnergyPrefs{EnergySources: json.RawMessage(`[]`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := energyJSON(t, partial); got != `{"device_consumption":[],"device_consumption_water":[],"energy_sources":[]}` {
		t.Errorf("partial prefs = %s", got)
	}
}

// After a save, a read that returns what HA stored (which differs from what the user wrote) is
// not drift: state keeps the user's values. A read that differs from what HA stored is.
func TestEnergyPreferencesReadAgainstBaseline(t *testing.T) {
	f := &fakeEnergy{rewrite: map[string]any{"filled_in_by_ha": "default"}}
	r := &energyPreferencesResource{energy: client.NewEnergy(f)}
	private := fakePrivate{}
	plan := energyPlan(t)

	var diags diag.Diagnostics
	r.save(t.Context(), plan, private, &diags)
	if diags.HasError() || len(diags) != 0 {
		t.Fatal(diags)
	}
	if want := []string{"energy/save_prefs", "energy/get_prefs"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q (save, then read back for the baseline)", f.calls, want)
	}
	if private[baselineKey] == nil {
		t.Fatal("no baseline stored")
	}

	state := plan
	state.ID = types.StringValue("energy_preferences")
	got, ok := r.read(t.Context(), state, private, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("read: ok = %v, %v", ok, diags)
	}
	if want := energyJSON(t, state); energyJSON(t, got) != want {
		t.Errorf("read after save = %s, want the user's values %s", energyJSON(t, got), want)
	}

	// The same read without a baseline (after import) shows what HA stored.
	got, _ = r.read(t.Context(), state, fakePrivate{}, &diags)
	if eq, _ := state.EnergySources.DynamicSemanticEquals(t.Context(), got.EnergySources); eq {
		t.Errorf("without a baseline, HA's rewritten sources %s equal the user's", energyHASources)
	}

	// Edited in the HA UI: a device added.
	f.stored["device_consumption"] = json.RawMessage(`[{"stat_consumption":"sensor.oven_energy"},{"stat_consumption":"sensor.dryer_energy"}]`)
	got, _ = r.read(t.Context(), state, private, &diags)
	if eq, _ := state.DeviceConsumption.DynamicSemanticEquals(t.Context(), got.DeviceConsumption); eq {
		t.Error("a device added in HA is not drift")
	}
}

func TestEnergyPreferencesReadWithoutPrefsRemovesResource(t *testing.T) {
	r := &energyPreferencesResource{energy: client.NewEnergy(&fakeEnergy{})}
	state := energyPlan(t)
	var diags diag.Diagnostics
	if _, ok := r.read(t.Context(), state, fakePrivate{}, &diags); ok || diags.HasError() {
		t.Errorf("read without stored preferences: ok = %v, %v; want ok = false and no error", ok, diags)
	}
}

func TestEnergyPreferencesCreateRefusesExistingPreferences(t *testing.T) {
	create := func(f *fakeEnergy) diag.Diagnostics {
		r := &energyPreferencesResource{energy: client.NewEnergy(f)}
		planned := stateOf(t, r, energyPlan(t))
		resp := resource.CreateResponse{State: tfsdk.State{Schema: planned.Schema, Raw: tftypes.NewValue(planned.Raw.Type(), nil)}}
		r.Create(t.Context(), resource.CreateRequest{Plan: tfsdk.Plan(planned)}, &resp)
		return resp.Diagnostics
	}

	existing := &fakeEnergy{stored: map[string]json.RawMessage{
		"energy_sources":           json.RawMessage(energyUserSources),
		"device_consumption":       json.RawMessage(`[]`),
		"device_consumption_water": json.RawMessage(`[]`),
	}}
	checkDiags(t, create(existing), "overwrite them")
	if slices.Contains(existing.calls, "energy/save_prefs") {
		t.Error("create saved over existing preferences")
	}
}

func TestEnergyPreferencesDeleteResets(t *testing.T) {
	f := &fakeEnergy{stored: map[string]json.RawMessage{"energy_sources": json.RawMessage(energyUserSources)}}
	r := &energyPreferencesResource{energy: client.NewEnergy(f)}
	var resp resource.DeleteResponse
	r.Delete(t.Context(), resource.DeleteRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	for _, k := range []string{"energy_sources", "device_consumption", "device_consumption_water"} {
		if string(f.stored[k]) != "[]" {
			t.Errorf("%s = %s after delete, want []", k, f.stored[k])
		}
	}
}

func TestEnergyPrefsEmpty(t *testing.T) {
	cases := map[string]struct {
		prefs client.EnergyPrefs
		empty bool
	}{
		"zero value":   {client.EnergyPrefs{}, true},
		"empty lists":  {client.EnergyPrefs{EnergySources: json.RawMessage(`[]`), DeviceConsumption: json.RawMessage(`[]`)}, true},
		"null":         {client.EnergyPrefs{EnergySources: json.RawMessage(`null`)}, true},
		"a source":     {client.EnergyPrefs{EnergySources: json.RawMessage(energyUserSources)}, false},
		"a water user": {client.EnergyPrefs{DeviceConsumptionWater: json.RawMessage(`[{"stat_consumption":"sensor.tap"}]`)}, false},
	}
	for name, tc := range cases {
		if got := energyPrefsEmpty(tc.prefs); got != tc.empty {
			t.Errorf("%s: empty = %v, want %v", name, got, tc.empty)
		}
	}
}

func TestEnergyPreferencesImportRejectsOtherIDs(t *testing.T) {
	r := &energyPreferencesResource{}
	var resp resource.ImportStateResponse
	r.ImportState(t.Context(), resource.ImportStateRequest{ID: "other"}, &resp)
	checkDiags(t, resp.Diagnostics, `"energy_preferences"`)
}
