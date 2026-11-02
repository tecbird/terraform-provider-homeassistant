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

// inputs is the `steps` and `sensitive_steps` of a `homeassistant_integration`.
type inputs struct{ steps, sensitive dyntype.Value }

// integrationData returns the schema and a raw `homeassistant_integration` with the given inputs,
// or a null object for nil inputs.
func integrationData(t *testing.T, in *inputs) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	ctx := t.Context()
	var sr resource.SchemaResponse
	NewIntegration().Schema(ctx, resource.SchemaRequest{}, &sr)
	s := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	if in != nil {
		if d := s.SetAttribute(ctx, stepsPath, in.steps); d.HasError() {
			t.Fatal(d)
		}
		if d := s.SetAttribute(ctx, sensitiveStepsPath, in.sensitive); d.HasError() {
			t.Fatal(d)
		}
	}
	return sr, s.Raw
}

// Changed steps replace the entry only if the prior and the planned inputs each hold a step, in
// `steps` or `sensitive_steps`: otherwise the provider does not know the steps, after an import or
// after both were removed.
func TestStepsChanged(t *testing.T) {
	steps := mustDyn(t, `{"user": {"host": "a"}}`)
	changed := mustDyn(t, `{"user": {"host": "b"}}`)
	secret := mustDyn(t, `{"user": {"api_key": "a"}}`)
	secretChanged := mustDyn(t, `{"user": {"api_key": "b"}}`)
	null := dyntype.NewNull()
	empty := mustDyn(t, `{}`)
	unknown := dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}
	cases := map[string]struct {
		prior, planned inputs
		want           bool
	}{
		"changed":                    {prior: inputs{steps, null}, planned: inputs{changed, null}, want: true},
		"to unknown":                 {prior: inputs{steps, null}, planned: inputs{unknown, null}, want: true},
		"after import":               {prior: inputs{null, null}, planned: inputs{changed, null}},
		"after import, empty step":   {prior: inputs{null, null}, planned: inputs{mustDyn(t, `{"user": {}}`), null}},
		"after import, secrets only": {prior: inputs{null, null}, planned: inputs{null, secret}},
		"removed":                    {prior: inputs{steps, null}, planned: inputs{null, null}},
		"removed, secrets left {}":   {prior: inputs{steps, secret}, planned: inputs{null, empty}},
		"null to null":               {prior: inputs{null, null}, planned: inputs{null, null}},
		"secrets only, changed":      {prior: inputs{null, secret}, planned: inputs{null, secretChanged}, want: true},
		"secrets only, removed":      {prior: inputs{null, secret}, planned: inputs{null, null}},
		"steps moved to secrets":     {prior: inputs{steps, null}, planned: inputs{null, secret}, want: true},
		"steps removed, secrets":     {prior: inputs{steps, secret}, planned: inputs{null, secret}, want: true},
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

// A plan that creates the entry needs a step, in `steps` or `sensitive_steps`; updating or
// destroying an imported one does not.
func TestIntegrationModifyPlan(t *testing.T) {
	steps := mustDyn(t, `{"user": {}}`)
	secret := mustDyn(t, `{"user": {"api_key": "hunter2"}}`)
	null := dyntype.NewNull()
	empty := mustDyn(t, `{}`)
	unknown := dyntype.Value{DynamicValue: basetypes.NewDynamicUnknown()}
	cases := map[string]struct {
		state, plan *inputs // nil: no object, i.e. a create or a destroy
		wantErr     bool
	}{
		"create with steps":           {plan: &inputs{steps, null}},
		"create with secrets only":    {plan: &inputs{null, secret}},
		"create with unknown steps":   {plan: &inputs{unknown, null}},
		"create with unknown secrets": {plan: &inputs{null, unknown}},
		"create without steps":        {plan: &inputs{null, null}, wantErr: true},
		"create with empty secrets":   {plan: &inputs{null, empty}, wantErr: true},
		"update without steps":        {state: &inputs{null, null}, plan: &inputs{null, null}},
		"forget steps":                {state: &inputs{steps, secret}, plan: &inputs{null, null}},
		"destroy without steps":       {state: &inputs{null, null}},
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
