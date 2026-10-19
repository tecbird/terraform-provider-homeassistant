package resources

import (
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Home Assistant's defaults for a counter.
const (
	counterInitialDefault = 0
	counterStepDefault    = 1
	counterRestoreDefault = true
)

// NewCounter returns the `homeassistant_counter` resource.
func NewCounter() resource.Resource {
	return newHelper(helperSpec{
		domain:      "counter",
		description: "A counter helper (`counter`): an integer that automations count up or down.",
		fields: map[string]schema.Attribute{
			"initial": schema.Int64Attribute{
				Description: "Value the counter starts with when Home Assistant starts and when it is reset, " +
					"between `minimum` and `maximum`. Defaults to `0`. The provider never manages the " +
					"current value.",
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(counterInitialDefault),
			},
			"minimum": schema.Int64Attribute{
				Description: "Lowest value. Unset, the counter has no lower bound.",
				Optional:    true,
			},
			"maximum": schema.Int64Attribute{
				Description: "Highest value. Must not be less than `minimum`. Unset, the counter has no " +
					"upper bound.",
				Optional: true,
			},
			"step": schema.Int64Attribute{
				Description: "Amount that one increment or decrement changes the value by. Must not be " +
					"negative. Defaults to `1`.",
				Optional: true,
				Computed: true,
				Default:  int64default.StaticInt64(counterStepDefault),
			},
			"restore": schema.BoolAttribute{
				Description: "Whether the counter restores its value after a restart of Home Assistant. " +
					"Defaults to `true`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(counterRestoreDefault),
			},
		},
		validate: validateCounter,
	})
}

// validateCounter checks `minimum ≤ maximum`, `step ≥ 0`, and that a configured `initial` is
// within the range. An unset `initial` is not checked: HA clamps its default into the range.
func validateCounter(attrs map[string]attr.Value) diag.Diagnostics {
	var diags diag.Diagnostics
	lo, loSet, loKnown := configValue[types.Int64](attrs, "minimum")
	hi, hiSet, hiKnown := configValue[types.Int64](attrs, "maximum")
	loOK, hiOK := loSet && loKnown, hiSet && hiKnown
	if loOK && hiOK && lo.ValueInt64() > hi.ValueInt64() {
		diags.AddAttributeError(path.Root("maximum"), "Invalid Attribute Value", fmt.Sprintf(
			"maximum must not be less than minimum, got minimum = %d and maximum = %d.", lo.ValueInt64(), hi.ValueInt64()))
	}
	if step, set, known := configValue[types.Int64](attrs, "step"); set && known && step.ValueInt64() < 0 {
		diags.AddAttributeError(path.Root("step"), "Invalid Attribute Value", fmt.Sprintf(
			"step must not be negative, got %d.", step.ValueInt64()))
	}
	if initial, set, known := configValue[types.Int64](attrs, "initial"); set && known {
		v := initial.ValueInt64()
		if loOK && v < lo.ValueInt64() {
			diags.AddAttributeError(path.Root("initial"), "Invalid Attribute Value", fmt.Sprintf(
				"initial must not be less than minimum (%d), got %d.", lo.ValueInt64(), v))
		}
		if hiOK && v > hi.ValueInt64() {
			diags.AddAttributeError(path.Root("initial"), "Invalid Attribute Value", fmt.Sprintf(
				"initial must not be more than maximum (%d), got %d.", hi.ValueInt64(), v))
		}
	}
	return diags
}
