package resources

import (
	"context"
	"encoding/json"
	"maps"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func TestHelperFieldsLeavesOutUnset(t *testing.T) {
	typ := helperType(t, NewInputBoolean())
	obj := types.ObjectValueMust(typ.AttrTypes, map[string]attr.Value{
		"id":        types.StringValue("guest_mode"),
		"entity_id": types.StringUnknown(),
		"name":      types.StringValue("Guest mode"),
		"icon":      types.StringNull(),
		"initial":   types.BoolValue(false),
	})
	got, err := helperFields(obj)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "Guest mode", "initial": false}
	if !maps.Equal(got, want) {
		t.Errorf("fields = %v, want %v", got, want)
	}
}

func TestHelperObject(t *testing.T) {
	typ := helperType(t, NewInputBoolean())
	r := NewInputBoolean().(*helperResource)
	got, diags := r.helperObject(typ, client.Helper{"id": "guest_mode", "name": "Guest mode", "initial": true})
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := types.ObjectValueMust(typ.AttrTypes, map[string]attr.Value{
		"id":        types.StringValue("guest_mode"),
		"entity_id": types.StringValue("input_boolean.guest_mode"),
		"name":      types.StringValue("Guest mode"),
		"icon":      types.StringNull(),
		"initial":   types.BoolValue(true),
	})
	if !got.Equal(want) {
		t.Errorf("object = %v, want %v", got, want)
	}
}

// helperType returns the object type of the helper resource r.
func helperType(t *testing.T, r resource.Resource) types.ObjectType {
	t.Helper()
	resp := &resource.SchemaResponse{}
	r.Schema(context.Background(), resource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	return resp.Schema.Type().(types.ObjectType)
}

// TestHelperRoundTrip converts typed attributes (numbers and lists) to HA fields and, after
// decoding them as JSON like HA's reply, back.
func TestHelperRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		r     resource.Resource
		attrs map[string]attr.Value
	}{
		{NewInputNumber(), map[string]attr.Value{
			"min": types.Float64Value(-2.5), "max": types.Float64Value(30), "step": types.Float64Value(0.1),
			"initial": types.Float64Null(), "mode": types.StringValue("box"), "unit_of_measurement": types.StringValue("°C"),
		}},
		{NewInputText(), map[string]attr.Value{
			"min": types.Int64Value(0), "max": types.Int64Value(255), "initial": types.StringValue("hi"),
			"pattern": types.StringNull(), "mode": types.StringValue("password"),
		}},
		{NewInputSelect(), map[string]attr.Value{"options": strs("off", "eco", "comfort"), "initial": types.StringValue("eco")}},
		{NewInputDatetime(), map[string]attr.Value{
			"has_date": types.BoolValue(true), "has_time": types.BoolValue(false), "initial": types.StringNull(),
		}},
		{NewInputButton(), map[string]attr.Value{}},
		{NewCounter(), map[string]attr.Value{
			"initial": types.Int64Value(5), "minimum": types.Int64Value(-3), "maximum": types.Int64Null(),
			"step": types.Int64Value(2), "restore": types.BoolValue(false),
		}},
		{NewTimer(), map[string]attr.Value{"duration": types.StringValue("0:05:00"), "restore": types.BoolValue(true)}},
		{NewSchedule(), scheduleAttrs(map[string][][2]string{
			"monday":  {{"07:00:00", "12:00:00"}, {"13:00:00", "24:00:00"}},
			"tuesday": {},
		})},
	} {
		r := tc.r.(*helperResource)
		t.Run(r.spec.domain, func(t *testing.T) {
			typ := helperType(t, r)
			vals := map[string]attr.Value{
				"id":        types.StringValue("thing"),
				"entity_id": types.StringValue(r.spec.domain + ".thing"),
				"name":      types.StringValue("Thing"),
				"icon":      types.StringNull(),
			}
			maps.Copy(vals, tc.attrs)
			obj := types.ObjectValueMust(typ.AttrTypes, vals)

			fields, err := helperFields(obj)
			if err != nil {
				t.Fatal(err)
			}
			fields["id"] = "thing"
			b, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var h client.Helper
			if err := json.Unmarshal(b, &h); err != nil {
				t.Fatal(err)
			}
			got, diags := r.helperObject(typ, h)
			if diags.HasError() {
				t.Fatal(diags)
			}
			if !got.Equal(obj) {
				t.Errorf("round trip = %v, want %v", got, obj)
			}
		})
	}
}

// scheduleAttrs returns the weekday attributes of a schedule. Days not in ranges have no ranges.
func scheduleAttrs(ranges map[string][][2]string) map[string]attr.Value {
	attrs := map[string]attr.Value{}
	for _, day := range scheduleDays {
		elems := []attr.Value{}
		for _, r := range ranges[day] {
			elems = append(elems, types.ObjectValueMust(scheduleTimeRangeType.AttrTypes, map[string]attr.Value{
				scheduleFrom: types.StringValue(r[0]),
				scheduleTo:   types.StringValue(r[1]),
				scheduleData: dyntype.NewJSONStringNull(),
			}))
		}
		attrs[day] = types.ListValueMust(scheduleTimeRangeType, elems)
	}
	return attrs
}

// TestHelperObjectSchedule reads a schedule as HA reports it. HA reports all seven days.
func TestHelperObjectSchedule(t *testing.T) {
	r := NewSchedule().(*helperResource)
	typ := helperType(t, r)
	h := client.Helper{
		"id": "tariff", "name": "Tariff",
		"monday": []any{map[string]any{"from": "22:00:00", "to": "24:00:00"}},
	}
	for _, day := range scheduleDays[1:] {
		h[day] = []any{}
	}
	got, diags := r.helperObject(typ, h)
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := map[string]attr.Value{
		"id": types.StringValue("tariff"), "entity_id": types.StringValue("schedule.tariff"),
		"name": types.StringValue("Tariff"), "icon": types.StringNull(),
	}
	maps.Copy(want, scheduleAttrs(map[string][][2]string{"monday": {{"22:00:00", "24:00:00"}}}))
	if !got.Equal(types.ObjectValueMust(typ.AttrTypes, want)) {
		t.Errorf("object = %v, want %v", got, want)
	}
}

// TestScheduleDataRoundTrip sends a range with data of every value type, decodes the JSON like
// HA's reply, and reads it back: the value read back equals the config semantically, so the
// framework keeps the prior value and the plan stays empty.
func TestScheduleDataRoundTrip(t *testing.T) {
	r := NewSchedule().(*helperResource)
	typ := helperType(t, r)
	// Key order and number spelling differ from what HA reports back.
	const data = `{"tier": "high", "price": 0.250, "peak": true, "level": 3}`
	rng := types.ObjectValueMust(scheduleTimeRangeType.AttrTypes, map[string]attr.Value{
		scheduleFrom: types.StringValue("18:00:00"), scheduleTo: types.StringValue("21:00:00"),
		scheduleData: dyntype.NewJSONString(data),
	})
	vals := scheduleAttrs(nil)
	vals["monday"] = types.ListValueMust(scheduleTimeRangeType, []attr.Value{rng})
	maps.Copy(vals, map[string]attr.Value{
		"id": types.StringValue("tariff"), "entity_id": types.StringValue("schedule.tariff"),
		"name": types.StringValue("Tariff"), "icon": types.StringNull(),
	})
	obj := types.ObjectValueMust(typ.AttrTypes, vals)

	fields, err := helperFields(obj)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(fields["monday"])
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"data":{"level":3,"peak":true,"price":0.25,"tier":"high"},"from":"18:00:00","to":"21:00:00"}]`
	if eq, err := dyntype.SemanticallyEqualJSON(b, []byte(want)); err != nil || !eq {
		t.Errorf("monday sent to HA = %s, want %s (err %v)", b, want, err)
	}

	fields["id"] = "tariff"
	b, _ = json.Marshal(fields)
	var h client.Helper
	if err := json.Unmarshal(b, &h); err != nil {
		t.Fatal(err)
	}
	got, diags := r.helperObject(typ, h)
	if diags.HasError() {
		t.Fatal(diags)
	}
	monday := got.Attributes()["monday"].(types.List).Elements()[0].(types.Object)
	gotData := monday.Attributes()[scheduleData].(dyntype.JSONString)
	eq, diags := dyntype.NewJSONString(data).StringSemanticEquals(context.Background(), gotData)
	if diags.HasError() || !eq {
		t.Errorf("data read back = %v, want semantically equal to %v (diags %v)", gotData, data, diags)
	}
}
