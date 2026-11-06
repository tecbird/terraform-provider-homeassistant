package resources

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// thresholdOptionsForm is the options form of a threshold entry: upper is 10, lower is unset.
const thresholdOptionsForm = `{"type":"form","flow_id":"o1","handler":"E1","step_id":"init","errors":null,"data_schema":[
	{"name":"hysteresis","required":true,"default":0.0,"description":{"suggested_value":0.0}},
	{"name":"lower","required":false,"description":{"suggested_value":null}},
	{"name":"upper","required":false,"description":{"suggested_value":10.0}},
	{"name":"entity_id","required":true,"description":{"suggested_value":"sensor.source"}}]}`

func thresholdOptionsFlowForm(t *testing.T) client.FlowForm {
	t.Helper()
	var res struct {
		StepID     string             `json:"step_id"`
		DataSchema []client.FormField `json:"data_schema"`
	}
	if err := json.Unmarshal([]byte(thresholdOptionsForm), &res); err != nil {
		t.Fatal(err)
	}
	return client.FlowForm{Flow: "options flow", StepID: res.StepID, Fields: res.DataSchema}
}

func stepsOf(t *testing.T, js string) optionSteps {
	t.Helper()
	steps, err := decodeOptionSteps(mustDyn(t, js))
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestReadOptionSteps(t *testing.T) {
	for name, tc := range map[string]struct {
		prior string
		want  string
	}{
		"import reads every field with a value": {
			prior: `null`,
			want:  `{"init":{"entity_id":"sensor.source","hysteresis":0.0,"upper":10.0}}`,
		},
		"only the fields in the config": {
			prior: `{"init":{"upper":20}}`,
			want:  `{"init":{"upper":10.0}}`,
		},
		"an unset option reads null": {
			prior: `{"init":{"lower":5}}`,
			want:  `{"init":{"lower":null}}`,
		},
		"a later step cannot be read": {
			prior: `{"init":{"upper":10},"second":{"x":1}}`,
			want:  `{"init":{"upper":10.0},"second":{"x":1}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var prior optionSteps
			if tc.prior != "null" {
				prior = stepsOf(t, tc.prior)
			}
			got, _ := json.Marshal(readOptionSteps(prior, thresholdOptionsFlowForm(t)))
			if string(got) != tc.want {
				t.Errorf("read = %s\nwant   %s", got, tc.want)
			}
		})
	}
}

func TestOptionsUnchanged(t *testing.T) {
	for steps, want := range map[string]bool{
		`{"init":{"upper":10,"hysteresis":0}}`:   true,
		`{"init":{"upper":10,"lower":null}}`:     true,
		`{"init":{"upper":20}}`:                  false,
		`{"init":{"lower":5}}`:                   false,
		`{"init":{"upper":10},"second":{"x":1}}`: false,
		`{"other":{"upper":10}}`:                 false,
	} {
		got, err := optionsUnchanged(stepsOf(t, steps), thresholdOptionsFlowForm(t))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("optionsUnchanged(%s) = %v, want %v", steps, got, want)
		}
	}
}

// fakeOptionsFlow serves the options flow of E1 with thresholdOptionsForm. It records each
// request as "start", "submit <data>" or "abort".
type fakeOptionsFlow struct {
	t        *testing.T
	requests []string
}

func (f *fakeOptionsFlow) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Method != http.MethodDelete {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			f.t.Errorf("%s %s: %v", r.Method, r.URL.Path, err)
		}
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /api/config/config_entries/options/flow":
		if body["handler"] != "E1" {
			f.t.Errorf("options flow started with %v", body)
		}
		f.requests = append(f.requests, "start")
		_, _ = w.Write([]byte(thresholdOptionsForm))
	case "POST /api/config/config_entries/options/flow/o1":
		data, _ := json.Marshal(body)
		f.requests = append(f.requests, "submit "+string(data))
		_, _ = w.Write([]byte(`{"type":"create_entry","flow_id":"o1","handler":"E1","result":true}`))
	case "DELETE /api/config/config_entries/options/flow/o1":
		f.requests = append(f.requests, "abort")
		_, _ = w.Write([]byte(`{"message":"Flow aborted"}`))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestIntegrationOptionsUpdate(t *testing.T) {
	for name, tc := range map[string]struct {
		steps   string
		want    []string
		wantErr string
	}{
		"already set": {
			// After an import the state holds every field; dropping some changes nothing in HA.
			steps: `{"init":{"upper":10}}`,
			want:  []string{"start", "abort"},
		},
		"changed": {
			steps: `{"init":{"upper":20}}`,
			want: []string{
				"start", "abort", "start",
				`submit {"entity_id":"sensor.source","hysteresis":0,"upper":20}`,
			},
		},
		"unknown field": {
			steps:   `{"init":{"uper":20}}`,
			want:    []string{"start", "abort"},
			wantErr: `step "init" of options flow of config entry E1 has no field "uper"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeOptionsFlow{t: t}
			srv := httptest.NewServer(f)
			defer srv.Close()
			u, _ := url.Parse(srv.URL)
			c := client.New(client.Config{URL: u, Token: "t", Timeout: 5 * time.Second})
			r := NewIntegrationOptions().(*integrationOptionsResource)
			r.Configure(t.Context(), resource.ConfigureRequest{ProviderData: c}, &resource.ConfigureResponse{})

			prior := integrationOptionsModel{
				EntryID: types.StringValue("E1"),
				Steps:   mustDyn(t, `{"init":{"upper":10,"hysteresis":0,"entity_id":"sensor.source"}}`),
				Domain:  types.StringValue("threshold"),
			}
			plan := prior
			plan.Steps = mustDyn(t, tc.steps)
			state, planned := stateOf(t, r, prior), stateOf(t, r, plan)
			resp := resource.UpdateResponse{State: state}
			r.Update(t.Context(), resource.UpdateRequest{Plan: tfsdk.Plan(planned), State: state}, &resp)

			if !slices.Equal(f.requests, tc.want) {
				t.Errorf("requests = %q\nwant       %q", f.requests, tc.want)
			}
			if tc.wantErr != "" {
				if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), tc.wantErr) {
					t.Errorf("diagnostics = %v, want %q", resp.Diagnostics, tc.wantErr)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			if !resp.State.Raw.Equal(planned.Raw) {
				t.Errorf("state = %v, want the plan", resp.State.Raw)
			}
		})
	}
}
