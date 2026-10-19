package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

// scheduleDays are the weekdays of a schedule, in the order HA lists them. Each is an attribute
// of the resource and a field of the schedule in HA.
var scheduleDays = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// Attribute names of a time range.
const (
	scheduleFrom = "from"
	scheduleTo   = "to"
	// scheduleData holds custom values of a time range (bool, string, int, or float), which show
	// up in the entity's attributes while the range is active.
	scheduleData = "data"
)

var (
	// scheduleTime matches the one format HA stores a time of day in. HA also accepts `07:00`,
	// but stores it as `07:00:00`, which would make the value HA reports differ from the config.
	scheduleTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]$`)
	// scheduleEndTime also matches `24:00:00`, the only time of day above 23:59:59 that HA
	// accepts, as the end of a range.
	scheduleEndTime = regexp.MustCompile(`^(([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9]|24:00:00)$`)
	// scheduleTimeRangeType is the type of one entry of a weekday's list.
	scheduleTimeRangeType = types.ObjectType{AttrTypes: map[string]attr.Type{
		scheduleFrom: types.StringType,
		scheduleTo:   types.StringType,
		scheduleData: dyntype.JSONStringType{},
	}}
)

// NewSchedule returns the `homeassistant_schedule` resource.
func NewSchedule() resource.Resource {
	fields := make(map[string]schema.Attribute, len(scheduleDays))
	for _, day := range scheduleDays {
		fields[day] = schema.ListNestedAttribute{
			Description: fmt.Sprintf("Time ranges on %s, in order of `from`. The ranges must not overlap. "+
				"Defaults to none.", strings.ToUpper(day[:1])+day[1:]),
			Optional: true,
			Computed: true,
			Default:  listdefault.StaticValue(types.ListValueMust(scheduleTimeRangeType, nil)),
			NestedObject: schema.NestedAttributeObject{
				Attributes: map[string]schema.Attribute{
					scheduleFrom: schema.StringAttribute{
						Description: "Start of the range, as `HH:MM:SS` (24 hours), e.g. `07:00:00`. The schedule is on from here.",
						Required:    true,
						Validators: []validator.String{stringvalidator.RegexMatches(scheduleTime,
							"must have the format HH:MM:SS, e.g. 07:00:00")},
					},
					scheduleTo: schema.StringAttribute{
						Description: "End of the range, as `HH:MM:SS` (24 hours), later than `from`, e.g. " +
							"`22:00:00`. Use `24:00:00` for the end of the day. The schedule is off from here.",
						Required: true,
						Validators: []validator.String{stringvalidator.RegexMatches(scheduleEndTime,
							"must have the format HH:MM:SS, e.g. 22:00:00, or be 24:00:00")},
					},
					scheduleData: schema.StringAttribute{
						Description: "Custom values of the range, as JSON text, e.g. " +
							"`jsonencode({ price = 0.12, peak = true })`. It must be an object whose values are " +
							"strings, numbers or booleans. Home Assistant shows them as attributes of the entity " +
							"while the range is active. Unset, the range has none. The order of keys and the " +
							"spelling of numbers do not matter.",
						Optional:   true,
						CustomType: dyntype.JSONStringType{},
						Validators: []validator.String{scheduleDataValidator{}},
					},
				},
			},
		}
	}
	return newHelper(helperSpec{
		domain: "schedule",
		description: "A schedule helper (`schedule`): weekly time ranges in which the schedule is on, " +
			"e.g. a tariff window. Its state is `on` inside a range and `off` outside.",
		fields:   fields,
		validate: validateSchedule,
	})
}

// validateSchedule checks, for every weekday, that each range ends after it starts, that the
// ranges are in order of their start, and that they do not overlap (ranges that meet, one ending where the next starts, are fine). Times
// that do not have the right format are reported by the attribute validators, and skipped here.
func validateSchedule(attrs map[string]attr.Value) diag.Diagnostics {
	var diags diag.Diagnostics
	for _, day := range scheduleDays {
		ranges, set, known := configValue[types.List](attrs, day)
		if !set || !known {
			continue
		}
		diags.Append(validateScheduleDay(day, ranges)...)
	}
	return diags
}

// timeRange is one entry of a weekday's list.
type timeRange struct {
	index    int
	from, to string
}

func validateScheduleDay(day string, ranges types.List) diag.Diagnostics {
	var diags diag.Diagnostics
	var valid []timeRange
	for i, e := range ranges.Elements() {
		obj, ok := e.(types.Object)
		if !ok || obj.IsNull() || obj.IsUnknown() {
			return diags
		}
		from, fromOK := obj.Attributes()[scheduleFrom].(types.String)
		to, toOK := obj.Attributes()[scheduleTo].(types.String)
		if !fromOK || !toOK || from.IsUnknown() || to.IsUnknown() || from.IsNull() || to.IsNull() {
			return diags
		}
		f, t := from.ValueString(), to.ValueString()
		if !scheduleTime.MatchString(f) || !scheduleEndTime.MatchString(t) {
			continue
		}
		if f >= t {
			diags.AddAttributeError(path.Root(day).AtListIndex(i).AtName(scheduleTo), "Invalid Attribute Value",
				fmt.Sprintf("to must be later than from, got from = %s and to = %s.", f, t))
			continue
		}
		valid = append(valid, timeRange{index: i, from: f, to: t})
	}
	// The format is fixed-width, so strings compare like times of day. HA stores the ranges of a
	// day sorted by start, so a list in another order would read back differently.
	for i := 1; i < len(valid); i++ {
		prev, cur := valid[i-1], valid[i]
		switch {
		case cur.from < prev.from:
			diags.AddAttributeError(path.Root(day).AtListIndex(cur.index).AtName(scheduleFrom), "Invalid Attribute Value",
				fmt.Sprintf("the ranges must be listed in order of from, but %s-%s comes after %s-%s.",
					cur.from, cur.to, prev.from, prev.to))
		case prev.to > cur.from:
			diags.AddAttributeError(path.Root(day).AtListIndex(cur.index).AtName(scheduleFrom), "Invalid Attribute Value",
				fmt.Sprintf("the range %s-%s overlaps the range %s-%s.", cur.from, cur.to, prev.from, prev.to))
		}
	}
	return diags
}

// scheduleDataValidator checks that `data` is a JSON object of strings, numbers and booleans,
// the values HA accepts.
type scheduleDataValidator struct{}

func (scheduleDataValidator) Description(context.Context) string {
	return "must be a JSON object whose values are strings, numbers or booleans"
}

func (v scheduleDataValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v scheduleDataValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	dec := json.NewDecoder(strings.NewReader(req.ConfigValue.ValueString()))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || dec.More() || obj == nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid Attribute Value",
			fmt.Sprintf("data %s, got %q.", v.Description(ctx), req.ConfigValue.ValueString()))
		return
	}
	for k, e := range obj {
		switch e.(type) {
		case string, bool, json.Number:
		default:
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid Attribute Value",
				fmt.Sprintf("data %s, but %q is %T.", v.Description(ctx), k, e))
		}
	}
}
