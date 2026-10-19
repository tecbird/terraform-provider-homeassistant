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
		// Created but not identified: never suggest applying again, name what to import.
		"unidentified": {
			err: &client.SubentryUnidentifiedError{
				EntryID: "E1", SubentryType: "plane", Title: "30° / 180° / 5000W", Candidates: []string{"S2", "S3"},
				Err: client.ErrConnectionLost,
			},
			hint: "Do not apply again: that would create a second one. Import it instead: list the subentries " +
				"of config entry E1 with the WebSocket command `config_entries/subentries/list`, find the one " +
				"of type \"plane\" titled \"30° / 180° / 5000W\", and run `tofu import <resource address> " +
				"E1/<subentry_id>`. Candidates: S2, S3.",
		},
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
		"steps removed":           {state: model(steps, null, types.BoolValue(true)), plan: model(null, null, types.BoolValue(true))},
		// Only sensitive_steps set: they are the steps (ADR-0021), not an import.
		"secret-only changed":      {state: model(null, steps, types.BoolValue(true)), plan: model(null, changed, types.BoolValue(true)), want: true},
		"secret-only unchanged":    {state: model(null, steps, types.BoolValue(true)), plan: model(null, steps, types.BoolValue(true))},
		"secret moved to steps":    {state: model(null, steps, types.BoolValue(true)), plan: model(steps, null, types.BoolValue(true)), want: true},
		"secret-only after import": {state: model(null, null, types.BoolValue(true)), plan: model(null, changed, types.BoolValue(true))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := reconfigures(tc.state, tc.plan); got != tc.want {
				t.Errorf("reconfigures = %v, want %v", got, tc.want)
			}
		})
	}
}

// subentryData returns the schema and a raw `homeassistant_integration_subentry` with the given
// steps and supports_reconfigure.
func subentryData(t *testing.T, steps dyntype.Value, reconfigurable types.Bool) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	return subentrySecretData(t, steps, dyntype.NewNull(), reconfigurable)
}

// subentrySecretData is subentryData with sensitive_steps.
func subentrySecretData(t *testing.T, steps, sensitive dyntype.Value, reconfigurable types.Bool) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	ctx := t.Context()
	var sr resource.SchemaResponse
	NewIntegrationSubentry().Schema(ctx, resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	if d := s.SetAttribute(ctx, stepsPath, steps); d.HasError() {
		t.Fatal(d)
	}
	if d := s.SetAttribute(ctx, sensitiveStepsPath, sensitive); d.HasError() {
		t.Fatal(d)
	}
	if d := s.SetAttribute(ctx, supportsReconfigurePath, reconfigurable); d.HasError() {
		t.Fatal(d)
	}
	return sr, s.Raw
}

// Changed steps replace the subentry only if its type has no reconfigure flow, and never right
// after an import or when they are removed.
func TestSubentryStepsChanged(t *testing.T) {
	ctx := t.Context()
	steps := mustDyn(t, `{"user": {"declination": 30}}`)
	changed := mustDyn(t, `{"user": {"declination": 35}}`)
	null := dyntype.NewNull()
	cases := map[string]struct {
		prior, planned             dyntype.Value
		priorSecret, plannedSecret dyntype.Value // null if not set
		reconfigurable             types.Bool
		want                       bool
	}{
		"reconfigurable":              {prior: steps, planned: changed, reconfigurable: types.BoolValue(true)},
		"not reconfigurable":          {prior: steps, planned: changed, reconfigurable: types.BoolValue(false), want: true},
		"flag not read":               {prior: steps, planned: changed, reconfigurable: types.BoolNull(), want: true},
		"after import":                {prior: null, planned: changed, reconfigurable: types.BoolValue(false)},
		"after import, flag":          {prior: null, planned: changed, reconfigurable: types.BoolValue(true)},
		"removed, not reconfigurable": {prior: steps, planned: null, reconfigurable: types.BoolValue(false)},
		"removed, flag not read":      {prior: steps, planned: null, reconfigurable: types.BoolNull()},
		// Only sensitive_steps set: a change replaces or reconfigures like one of steps.
		"secret-only, not reconfigurable": {
			prior: null, planned: null, priorSecret: steps, plannedSecret: changed,
			reconfigurable: types.BoolValue(false), want: true,
		},
		"secret-only, reconfigurable": {
			prior: null, planned: null, priorSecret: steps, plannedSecret: changed, reconfigurable: types.BoolValue(true),
		},
		"secret-only after import": {
			prior: null, planned: null, priorSecret: null, plannedSecret: changed, reconfigurable: types.BoolValue(false),
		},
	}
	orNull := func(v dyntype.Value) dyntype.Value {
		if v.IsNull() {
			return dyntype.NewNull()
		}
		return v
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr, prior := subentrySecretData(t, tc.prior, orNull(tc.priorSecret), tc.reconfigurable)
			_, planned := subentrySecretData(t, tc.planned, orNull(tc.plannedSecret), tc.reconfigurable)
			var resp dynamicplanmodifier.RequiresReplaceIfFuncResponse
			subentryStepsChanged(ctx, planmodifier.DynamicRequest{
				State: tfsdk.State{Schema: sr.Schema, Raw: prior},
				Plan:  tfsdk.Plan{Schema: sr.Schema, Raw: planned},
			}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if resp.RequiresReplace != tc.want {
				t.Errorf("RequiresReplace = %v, want %v", resp.RequiresReplace, tc.want)
			}
		})
	}
}

// A plan that creates the subentry needs steps; updating or destroying an imported one does not.
func TestSubentryModifyPlanRequiresStepsToCreate(t *testing.T) {
	steps := mustDyn(t, `{"user": {"declination": 30}}`)
	null := dyntype.NewNull()
	unknown := dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}
	cases := map[string]struct {
		state, plan *dyntype.Value // nil: no object, i.e. a create or a destroy
		planSecret  *dyntype.Value // sensitive_steps of the plan; nil: null
		wantErr     bool
	}{
		"create with steps":         {plan: &steps},
		"create with unknown steps": {plan: &unknown},
		"create without steps":      {plan: &null, wantErr: true},
		// A replacement is planned as a create, so this covers replacing a secret-only one too.
		"create with sensitive steps only": {plan: &null, planSecret: &steps},
		"create with unknown sensitive":    {plan: &null, planSecret: &unknown},
		"update without steps":             {state: &null, plan: &null},
		"forget steps":                     {state: &steps, plan: &null},
		"destroy without steps":            {state: &null},
	}
	raw := func(steps, secret *dyntype.Value) (resource.SchemaResponse, tftypes.Value) {
		sr, v := subentryData(t, null, types.BoolValue(true))
		if steps == nil {
			return sr, tftypes.NewValue(v.Type(), nil)
		}
		s := null
		if secret != nil {
			s = *secret
		}
		_, v = subentrySecretData(t, *steps, s, types.BoolValue(true))
		return sr, v
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr, state := raw(tc.state, nil)
			_, plan := raw(tc.plan, tc.planSecret)
			resp := resource.ModifyPlanResponse{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}
			(&integrationSubentryResource{}).ModifyPlan(t.Context(), resource.ModifyPlanRequest{
				State: tfsdk.State{Schema: sr.Schema, Raw: state},
				Plan:  tfsdk.Plan{Schema: sr.Schema, Raw: plan},
			}, &resp)
			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Fatalf("error = %v, want %v: %v", got, tc.wantErr, resp.Diagnostics)
			}
			if tc.wantErr && !strings.Contains(resp.Diagnostics[0].Detail(), "needs `steps`") {
				t.Errorf("detail = %q", resp.Diagnostics[0].Detail())
			}
		})
	}
}
