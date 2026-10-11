package resources

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Home Assistant's defaults for a timer.
const (
	timerDurationDefault = "0:00:00"
	timerRestoreDefault  = false
)

// timerDuration matches the one format HA stores a timer's duration in: hours (without leading
// zeros, and not limited to 23), then two-digit minutes and seconds. HA accepts other spellings
// too (`00:05:00`, `5:00`, seconds as a number) but stores them in this one, which would make
// the value HA reports differ from the one in the config.
var timerDuration = regexp.MustCompile(`^(0|[1-9][0-9]*):[0-5][0-9]:[0-5][0-9]$`)

// NewTimer returns the `homeassistant_timer` resource.
func NewTimer() resource.Resource {
	return newHelper(helperSpec{
		domain:      "timer",
		description: "A timer helper (`timer`): counts down a duration, so automations can react when it finishes.",
		fields: map[string]schema.Attribute{
			"duration": schema.StringAttribute{
				Description: "How long the timer runs when it is started without a duration, as `H:MM:SS`, " +
					"e.g. `0:05:00` for five minutes or `36:00:00` for a day and a half. Defaults to " +
					"`0:00:00`.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString(timerDurationDefault),
			},
			"restore": schema.BoolAttribute{
				Description: "Whether a running timer carries on after a restart of Home Assistant. " +
					"Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(timerRestoreDefault),
			},
		},
		validate: validateTimer,
	})
}

// validateTimer checks that `duration` has the format `H:MM:SS`.
func validateTimer(attrs map[string]attr.Value) diag.Diagnostics {
	var diags diag.Diagnostics
	if d, set, known := configValue[types.String](attrs, "duration"); set && known && !timerDuration.MatchString(d.ValueString()) {
		diags.AddAttributeError(path.Root("duration"), "Invalid Attribute Value", fmt.Sprintf(
			"duration must have the format H:MM:SS, e.g. 0:05:00, got %q.", d.ValueString()))
	}
	return diags
}
