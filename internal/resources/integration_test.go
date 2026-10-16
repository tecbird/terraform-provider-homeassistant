package resources

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

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

func TestFlowStepsListsAnswerRepeatedForms(t *testing.T) {
	steps := mustDyn(t, `{"user": [{"backend_type": "ollama"}, {"host": "h", "port": 11434}], "confirm": {}}`)
	sensitive := mustDyn(t, `{"user": [{}, {"api_key": "k"}]}`)

	got, diags := flowSteps(steps, sensitive)
	if diags.HasError() {
		t.Fatal(diags)
	}
	want := []client.FlowStep{
		{StepID: "confirm", Data: map[string]any{}},
		{StepID: "user", Data: map[string]any{"backend_type": "ollama"}, SensitiveData: map[string]any{}},
		{StepID: "user", Data: map[string]any{"host": "h", "port": float64(11434)}, SensitiveData: map[string]any{"api_key": "k"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flowSteps = %#v\nwant %#v", got, want)
	}
}

func TestFlowStepsListOnlyInSensitiveSteps(t *testing.T) {
	got, diags := flowSteps(mustDyn(t, `{"a": {}}`), mustDyn(t, `{"user": [{"x": 1}, {"y": 2}]}`))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if len(got) != 3 || got[1].StepID != "user" || got[2].StepID != "user" || got[2].SensitiveData["y"] != float64(2) {
		t.Errorf("flowSteps = %#v", got)
	}
}

func TestFlowStepsMismatchedFormCountIsAnError(t *testing.T) {
	cases := map[string][2]string{
		"list vs object":       {`{"user": [{}, {}]}`, `{"user": {"k": "hunter2"}}`},
		"list vs shorter list": {`{"user": [{}, {}]}`, `{"user": [{"k": "hunter2"}]}`},
		"empty list":           {`{"user": []}`, `null`},
		"list of non-objects":  {`{"user": ["x"]}`, `null`},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, diags := flowSteps(mustDyn(t, in[0]), mustDyn(t, in[1]))
			if !diags.HasError() {
				t.Fatal("want an error")
			}
			for _, d := range diags {
				if strings.Contains(d.Detail(), "hunter2") {
					t.Errorf("diagnostic leaks the sensitive value: %s", d.Detail())
				}
			}
		})
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
		"list of forms":        {value: mustDyn(t, `{"user": [{"a": 1}, {"b": 2}]}`)},
		"empty list":           {value: mustDyn(t, `{"user": []}`), wantErr: `Step "user" must have at least one form`},
		"list of non-objects":  {value: mustDyn(t, `{"user": [{"a": 1}, "x"]}`), wantErr: `Form 2 of step "user" must be an object of fields, got`},
		"secret list non-obj":  {value: mustDyn(t, `{"user": ["hunter2"]}`), sensitive: true, wantErr: `Form 1 of step "user" must be an object of fields.`},
		"no steps":             {value: mustDyn(t, `{}`), wantErr: "At least one step"},
		"no sensitive steps":   {value: mustDyn(t, `{}`), sensitive: true},
		"not an object":        {value: mustDyn(t, `["user"]`), wantErr: "Must be an object"},
		"step not an object":   {value: mustDyn(t, `{"user": "h"}`), wantErr: `Step "user" must be an object of fields or a list of such objects, got`},
		"secret not an object": {value: mustDyn(t, `{"user": "hunter2"}`), sensitive: true, wantErr: `Step "user" must be an object of fields or a list of such objects.`},
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
