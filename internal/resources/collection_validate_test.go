package resources

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestValidateCounter(t *testing.T) {
	i := types.Int64Value
	for _, tc := range []struct {
		name  string
		attrs map[string]attr.Value
		want  string
	}{
		{"valid", map[string]attr.Value{"minimum": i(0), "maximum": i(10), "step": i(2), "initial": i(10)}, ""},
		{"no bounds", map[string]attr.Value{"initial": i(-100)}, ""},
		{"minimum equals maximum", map[string]attr.Value{"minimum": i(5), "maximum": i(5), "initial": i(5)}, ""},
		{"minimum above maximum", map[string]attr.Value{"minimum": i(6), "maximum": i(5)}, "maximum must not be less than minimum"},
		{"negative step", map[string]attr.Value{"step": i(-1)}, "step must not be negative"},
		{"zero step", map[string]attr.Value{"step": i(0)}, ""},
		{"initial below", map[string]attr.Value{"minimum": i(1), "initial": i(0)}, "initial must not be less than minimum (1)"},
		{"initial above", map[string]attr.Value{"maximum": i(1), "initial": i(2)}, "initial must not be more than maximum (1)"},
		{"unknown maximum", map[string]attr.Value{"minimum": i(6), "maximum": types.Int64Unknown(), "initial": i(100)}, ""},
		{"null bounds", map[string]attr.Value{"minimum": types.Int64Null(), "maximum": types.Int64Null(), "initial": i(3)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) { checkDiags(t, validateCounter(tc.attrs), tc.want) })
	}
}

func TestValidateTimer(t *testing.T) {
	s := types.StringValue
	for _, tc := range []struct {
		name  string
		attrs map[string]attr.Value
		want  string
	}{
		{"five minutes", map[string]attr.Value{"duration": s("0:05:00")}, ""},
		{"default", map[string]attr.Value{"duration": s("0:00:00")}, ""},
		{"more than a day", map[string]attr.Value{"duration": s("36:00:00")}, ""},
		{"leading zero", map[string]attr.Value{"duration": s("00:05:00")}, "duration must have the format H:MM:SS"},
		{"no seconds", map[string]attr.Value{"duration": s("0:05")}, "duration must have the format H:MM:SS"},
		{"minutes above 59", map[string]attr.Value{"duration": s("0:60:00")}, "duration must have the format H:MM:SS"},
		{"seconds", map[string]attr.Value{"duration": s("300")}, "duration must have the format H:MM:SS"},
		{"empty", map[string]attr.Value{"duration": s("")}, "duration must have the format H:MM:SS"},
		{"unknown", map[string]attr.Value{"duration": types.StringUnknown()}, ""},
		{"null", map[string]attr.Value{"duration": types.StringNull()}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) { checkDiags(t, validateTimer(tc.attrs), tc.want) })
	}
}

func TestValidateSchedule(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ranges map[string][][2]string
		want   string
	}{
		{"no ranges", nil, ""},
		{"valid", map[string][][2]string{"monday": {{"07:00:00", "12:00:00"}, {"13:00:00", "24:00:00"}}}, ""},
		{"ranges that meet", map[string][][2]string{"friday": {{"00:00:00", "12:00:00"}, {"12:00:00", "18:00:00"}}}, ""},
		{"not in order", map[string][][2]string{"friday": {{"13:00:00", "14:00:00"}, {"07:00:00", "08:00:00"}}}, "must be listed in order of from, but 07:00:00-08:00:00 comes after 13:00:00-14:00:00"},
		{"same ranges on two days", map[string][][2]string{"monday": {{"07:00:00", "08:00:00"}}, "tuesday": {{"07:00:00", "08:00:00"}}}, ""},
		{"to equals from", map[string][][2]string{"monday": {{"07:00:00", "07:00:00"}}}, "to must be later than from"},
		{"to before from", map[string][][2]string{"monday": {{"08:00:00", "07:00:00"}}}, "to must be later than from"},
		{"overlap", map[string][][2]string{"sunday": {{"07:00:00", "14:00:00"}, {"13:00:00", "15:00:00"}}}, "range 13:00:00-15:00:00 overlaps the range 07:00:00-14:00:00"},
		{"overlap, one inside the other", map[string][][2]string{"sunday": {{"07:00:00", "20:00:00"}, {"08:00:00", "09:00:00"}}}, "overlaps"},
		// The attribute validators report the bad format; this check skips it.
		{"bad format is skipped", map[string][][2]string{"monday": {{"7:00", "06:00:00"}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) { checkDiags(t, validateSchedule(scheduleAttrs(tc.ranges)), tc.want) })
	}

	t.Run("unknown day", func(t *testing.T) {
		attrs := scheduleAttrs(map[string][][2]string{"monday": {{"08:00:00", "07:00:00"}}})
		attrs["monday"] = types.ListUnknown(scheduleTimeRangeType)
		checkDiags(t, validateSchedule(attrs), "")
	})
}

func TestScheduleTimeFormats(t *testing.T) {
	for _, tc := range []struct {
		s          string
		from, to   bool
		whatItIsOK string
	}{
		{"00:00:00", true, true, "midnight"},
		{"23:59:59", true, true, "last second"},
		{"24:00:00", false, true, "end of the day, only as `to`"},
		{"24:00:01", false, false, "past the end of the day"},
		{"07:00", false, false, "no seconds"},
		{"7:00:00", false, false, "no leading zero"},
		{"07:60:00", false, false, "minutes above 59"},
	} {
		if got := scheduleTime.MatchString(tc.s); got != tc.from {
			t.Errorf("from %q (%s): valid = %v, want %v", tc.s, tc.whatItIsOK, got, tc.from)
		}
		if got := scheduleEndTime.MatchString(tc.s); got != tc.to {
			t.Errorf("to %q (%s): valid = %v, want %v", tc.s, tc.whatItIsOK, got, tc.to)
		}
	}
}

func TestScheduleDataValidator(t *testing.T) {
	for _, tc := range []struct {
		name, data, want string
	}{
		{"all value types", `{"s": "x", "i": 1, "f": 0.5, "b": false}`, ""},
		{"empty object", `{}`, ""},
		{"not JSON", `price=1`, "must be a JSON object"},
		{"array", `[1]`, "must be a JSON object"},
		{"null", `null`, "must be a JSON object"},
		{"trailing text", `{} x`, "must be a JSON object"},
		{"nested object", `{"a": {"b": 1}}`, `"a" is map`},
		{"list value", `{"a": [1]}`, `"a" is []`},
		{"null value", `{"a": null}`, `"a" is <nil>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &validator.StringResponse{}
			scheduleDataValidator{}.ValidateString(context.Background(),
				validator.StringRequest{ConfigValue: types.StringValue(tc.data)}, resp)
			checkDiags(t, resp.Diagnostics, tc.want)
		})
	}
	resp := &validator.StringResponse{}
	scheduleDataValidator{}.ValidateString(context.Background(),
		validator.StringRequest{ConfigValue: types.StringUnknown()}, resp)
	checkDiags(t, resp.Diagnostics, "")
}
