package resources

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func mustDyn(t *testing.T, js string) dyntype.Value {
	t.Helper()
	v, err := dyntype.FromJSON([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestFlowStepsMergesSensitiveSteps(t *testing.T) {
	steps := mustDyn(t, `{"user": {"host": "h", "port": 80}, "confirm": {}}`)
	sensitive := mustDyn(t, `{"user": {"password": "hunter2"}, "credentials": {"token": "t"}}`)

	got, diags := flowSteps(steps, sensitive)
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := []client.FlowStep{
		{StepID: "confirm", Data: map[string]any{}},
		{StepID: "credentials", SensitiveData: map[string]any{"token": "t"}},
		{StepID: "user", Data: map[string]any{"host": "h", "port": float64(80)}, SensitiveData: map[string]any{"password": "hunter2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flowSteps = %#v\nwant %#v", got, want)
	}

	got, diags = flowSteps(steps, dyntype.NewNull())
	if diags.HasError() || len(got) != 2 || got[1].SensitiveData != nil {
		t.Errorf("without sensitive_steps: %#v, %v", got, diags)
	}
}

func TestStepsValidator(t *testing.T) {
	cases := map[string]struct {
		value     dyntype.Value
		sensitive bool
		wantErr   string
	}{
		"steps":                {value: mustDyn(t, `{"user": {"host": "h"}}`)},
		"empty step":           {value: mustDyn(t, `{"user": {}}`)},
		"null":                 {value: dyntype.NewNull()},
		"unknown":              {value: dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}},
		"no steps":             {value: mustDyn(t, `{}`), wantErr: "At least one step"},
		"no sensitive steps":   {value: mustDyn(t, `{}`), sensitive: true},
		"not an object":        {value: mustDyn(t, `["user"]`), wantErr: "Must be an object"},
		"step not an object":   {value: mustDyn(t, `{"user": "h"}`), wantErr: `Step "user" must be an object of fields, got`},
		"secret not an object": {value: mustDyn(t, `{"user": "hunter2"}`), sensitive: true, wantErr: `Step "user" must be an object of fields.`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var resp validator.DynamicResponse
			stepsValidator{sensitive: tc.sensitive}.ValidateDynamic(t.Context(),
				validator.DynamicRequest{ConfigValue: tc.value.DynamicValue}, &resp)
			if tc.wantErr == "" {
				if resp.Diagnostics.HasError() {
					t.Errorf("unexpected error: %v", resp.Diagnostics)
				}
				return
			}
			if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics[0].Detail(), tc.wantErr) {
				t.Fatalf("diagnostics = %v, want %q", resp.Diagnostics, tc.wantErr)
			}
			if tc.sensitive && strings.Contains(resp.Diagnostics[0].Detail(), "hunter2") {
				t.Errorf("diagnostic leaks the sensitive value: %s", resp.Diagnostics[0].Detail())
			}
		})
	}
}

// integrationData returns the schema and a raw `homeassistant_integration` with the given steps,
// or a null object for nil steps.
func integrationData(t *testing.T, steps *dyntype.Value) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	ctx := t.Context()
	var sr resource.SchemaResponse
	NewIntegration().Schema(ctx, resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	if steps != nil {
		if d := s.SetAttribute(ctx, stepsPath, *steps); d.HasError() {
			t.Fatal(d)
		}
	}
	return sr, s.Raw
}

// Changed steps replace the entry only if both the prior and the planned steps are set: null
// steps mean the provider does not know them, after an import or after they were removed.
func TestStepsChanged(t *testing.T) {
	steps := mustDyn(t, `{"user": {"host": "a"}}`)
	changed := mustDyn(t, `{"user": {"host": "b"}}`)
	null := dyntype.NewNull()
	unknown := dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}
	cases := map[string]struct {
		prior, planned dyntype.Value
		want           bool
	}{
		"changed":             {prior: steps, planned: changed, want: true},
		"to unknown":          {prior: steps, planned: unknown, want: true},
		"after import":        {prior: null, planned: changed},
		"after import, empty": {prior: null, planned: mustDyn(t, `{"user": {}}`)},
		"removed":             {prior: steps, planned: null},
		"null to null":        {prior: null, planned: null},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr, prior := integrationData(t, &tc.prior)
			_, planned := integrationData(t, &tc.planned)
			var resp dynamicplanmodifier.RequiresReplaceIfFuncResponse
			stepsChanged(t.Context(), planmodifier.DynamicRequest{
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

// A plan that creates the entry needs steps; updating or destroying an imported one does not.
func TestIntegrationModifyPlan(t *testing.T) {
	steps := mustDyn(t, `{"user": {}}`)
	null := dyntype.NewNull()
	unknown := dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}
	cases := map[string]struct {
		state, plan *dyntype.Value // nil: no object, i.e. a create or a destroy
		wantErr     bool
	}{
		"create with steps":         {plan: &steps},
		"create with unknown steps": {plan: &unknown},
		"create without steps":      {plan: &null, wantErr: true},
		"update without steps":      {state: &null, plan: &null},
		"forget steps":              {state: &steps, plan: &null},
		"destroy without steps":     {state: &null},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sr, state := integrationData(t, tc.state)
			_, plan := integrationData(t, tc.plan)
			var resp resource.ModifyPlanResponse
			(&integrationResource{}).ModifyPlan(t.Context(), resource.ModifyPlanRequest{
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
