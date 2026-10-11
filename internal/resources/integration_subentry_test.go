package resources

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func TestParseSubentryID(t *testing.T) {
	entryID, subentryID, err := parseSubentryID("01JENTRY/01JSUB")
	if err != nil || entryID != "01JENTRY" || subentryID != "01JSUB" {
		t.Errorf("parseSubentryID = %q, %q, %v", entryID, subentryID, err)
	}
	for _, id := range []string{"", "01JENTRY", "01JENTRY/", "/01JSUB", "a/b/c"} {
		if _, _, err := parseSubentryID(id); err == nil || !strings.Contains(err.Error(), "<entry_id>/<subentry_id>") {
			t.Errorf("parseSubentryID(%q) = %v, want an error", id, err)
		}
	}
}

func TestSubentryErrorDetail(t *testing.T) {
	cases := map[string]struct {
		err  error
		hint string
	}{
		"already configured": {
			err:  &client.FlowAbortedError{Flow: "subentry flow", Reason: "already_configured"},
			hint: "tofu import <resource address> <entry_id>/<subentry_id>",
		},
		// The unknown type wraps the 404, but gets its own hint, not the one for a missing entry.
		"unknown type": {
			err:  &client.SubentryTypeUnknownError{EntryID: "E1", SubentryType: "x", Err: client.ErrNotFound},
			hint: "Check `subentry_type`",
		},
		"entry gone": {
			err:  fmt.Errorf("config entry E9: %w", client.ErrNotFound),
			hint: "Check `entry_id`",
		},
		"form error": {
			err:  &client.FlowFormError{Flow: "subentry flow", StepID: "user", Errors: map[string]string{"base": "cannot_connect"}},
			hint: "could not connect",
		},
		"unreachable": {
			err:  client.ErrUnreachable,
			hint: "-refresh=false",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := subentryErrorDetail(tc.err)
			if !strings.HasPrefix(got, tc.err.Error()) || !strings.Contains(got, tc.hint) {
				t.Errorf("subentryErrorDetail = %q, want the error and %q", got, tc.hint)
			}
		})
	}
}

func TestReconfigureErrorDetail(t *testing.T) {
	cases := map[string]struct {
		err  error
		hint string
	}{
		"ambiguous": {err: &client.FlowFieldAmbiguousError{Flow: "f", StepID: "reconfigure", Field: "x"}, hint: "step named after"},
		"gone":      {err: fmt.Errorf("subentry S1: %w", client.ErrNotFound), hint: "next plan creates it again"},
		"other":     {err: client.ErrUnreachable, hint: "-refresh=false"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := reconfigureErrorDetail(tc.err); !strings.HasPrefix(got, tc.err.Error()) || !strings.Contains(got, tc.hint) {
				t.Errorf("reconfigureErrorDetail = %q, want the error and %q", got, tc.hint)
			}
		})
	}
}

func TestStepsEqual(t *testing.T) {
	a := mustDyn(t, `{"user": {"declination": 30, "azimuth": 180}}`)
	cases := map[string]struct {
		a, b dyntype.Value
		want bool
	}{
		"same":            {a: a, b: mustDyn(t, `{"user": {"azimuth": 180, "declination": 30.0}}`), want: true},
		"changed":         {a: a, b: mustDyn(t, `{"user": {"declination": 35, "azimuth": 180}}`)},
		"null and empty":  {a: dyntype.NewNull(), b: mustDyn(t, `{}`), want: true},
		"null and steps":  {a: dyntype.NewNull(), b: a},
		"unknown":         {a: a, b: dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}},
		"other step name": {a: a, b: mustDyn(t, `{"reconfigure": {"declination": 30, "azimuth": 180}}`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := stepsEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("stepsEqual = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReconfigures(t *testing.T) {
	steps := mustDyn(t, `{"user": {"declination": 30}}`)
	changed := mustDyn(t, `{"user": {"declination": 35}}`)
	model := func(steps, sensitive dyntype.Value, reconfigurable types.Bool) integrationSubentryModel {
		return integrationSubentryModel{Steps: steps, SensitiveSteps: sensitive, SupportsReconfigure: reconfigurable}
	}
	null := dyntype.NewNull()
	cases := map[string]struct {
		state, plan integrationSubentryModel
		want        bool
	}{
		"steps changed":           {state: model(steps, null, types.BoolValue(true)), plan: model(changed, null, types.BoolValue(true)), want: true},
		"sensitive steps changed": {state: model(steps, null, types.BoolValue(true)), plan: model(steps, changed, types.BoolValue(true)), want: true},
		"unchanged":               {state: model(steps, null, types.BoolValue(true)), plan: model(steps, null, types.BoolValue(true))},
		"no reconfigure flow":     {state: model(steps, null, types.BoolValue(false)), plan: model(changed, null, types.BoolValue(false))},
		"flag unknown":            {state: model(steps, null, types.BoolNull()), plan: model(changed, null, types.BoolNull())},
		"after import":            {state: model(null, null, types.BoolValue(true)), plan: model(changed, null, types.BoolValue(true))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := reconfigures(tc.state, tc.plan); got != tc.want {
				t.Errorf("reconfigures = %v, want %v", got, tc.want)
			}
		})
	}
}

// Changed steps replace the subentry only if its type has no reconfigure flow, and never right
// after an import.
func TestSubentryStepsChanged(t *testing.T) {
	ctx := t.Context()
	var sr resource.SchemaResponse
	NewIntegrationSubentry().Schema(ctx, resource.SchemaRequest{}, &sr)
	state := func(steps dyntype.Value, reconfigurable types.Bool) tfsdk.State {
		s := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		if d := s.SetAttribute(ctx, stepsPath, steps); d.HasError() {
			t.Fatal(d)
		}
		if d := s.SetAttribute(ctx, supportsReconfigurePath, reconfigurable); d.HasError() {
			t.Fatal(d)
		}
		return s
	}
	steps := mustDyn(t, `{"user": {"declination": 30}}`)
	cases := map[string]struct {
		state tfsdk.State
		want  bool
	}{
		"reconfigurable":     {state: state(steps, types.BoolValue(true))},
		"not reconfigurable": {state: state(steps, types.BoolValue(false)), want: true},
		"flag not read":      {state: state(steps, types.BoolNull()), want: true},
		"after import":       {state: state(dyntype.NewNull(), types.BoolValue(false))},
		"after import, flag": {state: state(dyntype.NewNull(), types.BoolValue(true))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var resp dynamicplanmodifier.RequiresReplaceIfFuncResponse
			subentryStepsChanged(ctx, planmodifier.DynamicRequest{State: tc.state}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if resp.RequiresReplace != tc.want {
				t.Errorf("RequiresReplace = %v, want %v", resp.RequiresReplace, tc.want)
			}
		})
	}
}
