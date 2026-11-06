package resources

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// appearingDevice is a WSCommander whose device is in the registry from the after-th `list` on.
type appearingDevice struct {
	after int
	lists int
}

func (f *appearingDevice) Command(_ context.Context, typ string, _ map[string]any, result any) error {
	if typ != "config/device_registry/list" {
		return &client.WSError{Code: "unknown_command", Message: typ}
	}
	f.lists++
	devices := []map[string]any{{"id": "other"}}
	if f.lists >= f.after {
		devices = append(devices, map[string]any{"id": "moon1", "name": "Moon", "identifiers": [][]string{{"moon", "x"}}})
	}
	b, _ := json.Marshal(devices)
	return json.Unmarshal(b, result)
}

func TestAwaitDeviceWaitsUntilItAppears(t *testing.T) {
	fastPolls(t)
	f := &appearingDevice{after: 4}
	r := &deviceSettingsResource{devices: client.Devices(f)}
	device, err := r.awaitDevice(context.Background(), "moon1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if f.lists != 4 || deref(device.Name) != "Moon" {
		t.Errorf("lists = %d, device = %+v", f.lists, device)
	}
}

func TestAwaitDeviceTimesOut(t *testing.T) {
	fastPolls(t)
	r := &deviceSettingsResource{devices: client.Devices(&appearingDevice{after: 1 << 30})}
	_, err := r.awaitDevice(context.Background(), "typo", 20*time.Millisecond)
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"device typo", "did not appear", "timeouts.create"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func deref(p *string) string {
	if p == nil {
		return "<null>"
	}
	return *p
}

func TestDeviceSettingsChanges(t *testing.T) {
	ctx := context.Background()
	labels := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("managed")})
	user := "user"
	managed := deviceSettingsModel{
		DeviceID:   types.StringValue("moon1"),
		NameByUser: types.StringValue("Moon"),
		Labels:     labels,
		Disabled:   types.BoolValue(true),
	}

	tests := []struct {
		name     string
		to, from deviceSettingsModel
		want     map[string]any
	}{
		{
			name: "create writes only the set fields",
			to:   managed,
			want: map[string]any{"name_by_user": "Moon", "labels": []string{"managed"}, "disabled_by": &user},
		},
		{
			name: "destroy resets only the set fields",
			from: managed,
			want: map[string]any{"name_by_user": nil, "labels": []string{}, "disabled_by": nil},
		},
		{
			name: "update writes what changed",
			to: func() deviceSettingsModel {
				m := managed
				m.NameByUser = types.StringNull()
				m.AreaID = types.StringValue("sky")
				m.Disabled = types.BoolValue(false)
				return m
			}(),
			from: managed,
			want: map[string]any{"name_by_user": nil, "area_id": "sky", "disabled_by": nil},
		},
		{
			name: "no changes",
			to:   managed,
			from: managed,
			want: map[string]any{},
		},
		{
			name: "false leaves a flag alone that the user did not set",
			to:   deviceSettingsModel{Disabled: types.BoolValue(false)},
			from: deviceSettingsModel{Disabled: types.BoolValue(false)},
			want: map[string]any{},
		},
		{
			name: "destroy leaves a false flag alone",
			from: deviceSettingsModel{Disabled: types.BoolValue(false)},
			want: map[string]any{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, diags := tt.to.changes(ctx, tt.from)
			if diags.HasError() {
				t.Fatal(diags)
			}
			if !maps.EqualFunc(got, tt.want, func(a, b any) bool {
				ja, _ := json.Marshal(a)
				jb, _ := json.Marshal(b)
				return string(ja) == string(jb)
			}) {
				t.Errorf("changes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceSettingsRefreshIsFieldGranular(t *testing.T) {
	integration, name, byUser, area := "integration", "Moon", "Night Sky", "sky"
	device := client.Device{
		ID: "moon1", Name: &name, NameByUser: &byUser, AreaID: &area, Labels: []string{"other"},
		DisabledBy: &integration, Identifiers: [][]client.RegistryString{{"moon", "x"}}, ConfigEntries: []string{"entry1"},
	}
	m := deviceSettingsModel{
		DeviceID:   types.StringValue("moon1"),
		NameByUser: types.StringValue("Old"),
		Disabled:   types.BoolValue(true),
	}
	if diags := m.refresh(context.Background(), device); diags.HasError() {
		t.Fatal(diags)
	}

	if m.NameByUser.ValueString() != "Night Sky" || m.Name.ValueString() != "Moon" {
		t.Errorf("managed and computed attributes not read: %+v", m)
	}
	if !m.AreaID.IsNull() || !m.Labels.IsNull() {
		t.Errorf("unmanaged attributes were read: %+v", m)
	}
	if m.Disabled.ValueBool() {
		t.Error(`disabled_by "integration" read as disabled = true`)
	}
	wantIDs := types.ListValueMust(types.ListType{ElemType: types.StringType}, []attr.Value{
		types.ListValueMust(types.StringType, []attr.Value{types.StringValue("moon"), types.StringValue("x")}),
	})
	if !m.Identifiers.Equal(wantIDs) {
		t.Errorf("identifiers = %v, want %v", m.Identifiers, wantIDs)
	}
	if len(m.ConfigEntries.Elements()) != 1 || !m.Manufacturer.IsNull() {
		t.Errorf("config_entries = %v, manufacturer = %v", m.ConfigEntries, m.Manufacturer)
	}
}

func TestDeviceSettingsComputedListsAreNeverNull(t *testing.T) {
	var m deviceSettingsModel
	if diags := m.computedFrom(context.Background(), client.Device{ID: "bare"}); diags.HasError() {
		t.Fatal(diags)
	}
	if m.Identifiers.IsNull() || m.ConfigEntries.IsNull() {
		t.Errorf("identifiers = %v, config_entries = %v, want empty lists", m.Identifiers, m.ConfigEntries)
	}
}
