package client

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

const optionsPath = "config/config_entries/options/flow"

// thresholdForm is the options form of a threshold entry as HA 2026.9 serializes it: the current
// options are the suggested values, `lower` is unset.
const thresholdForm = `{"type":"form","flow_id":"f1","handler":"E1","step_id":"init","errors":null,"data_schema":[
	{"name":"hysteresis","required":true,"default":0.0,"description":{"suggested_value":0.5},"selector":{"number":{"mode":"box","step":"any"}}},
	{"name":"lower","optional":true,"required":false,"description":{"suggested_value":null},"selector":{"number":{"mode":"box","step":"any"}}},
	{"name":"upper","optional":true,"required":false,"description":{"suggested_value":10.0},"selector":{"number":{"mode":"box","step":"any"}}},
	{"name":"entity_id","required":true,"description":{"suggested_value":"sensor.source"},"selector":{"entity":{"domain":"sensor"}}}]}`

// sectionForm has a section whose field keeps its default, and a field without any value.
const sectionForm = `{"type":"form","flow_id":"f1","step_id":"init","data_schema":[
	{"name":"name","required":false},
	{"name":"advanced","type":"expandable","required":true,"schema":[
		{"name":"interval","required":true,"default":30},
		{"name":"retries","required":false,"description":{"suggested_value":3}}]}]}`

func decodeForm(t *testing.T, js string) FlowForm {
	t.Helper()
	var res flowResult
	if err := json.Unmarshal([]byte(js), &res); err != nil {
		t.Fatal(err)
	}
	return FlowForm{Flow: "options flow", StepID: res.StepID, Fields: res.DataSchema}
}

func TestOptionsFormReadsAndAborts(t *testing.T) {
	f := &fakeFlow{t: t, path: optionsPath, init: thresholdForm}

	form, err := NewOptionsFlows(serve(t, f.handle)).Form(t.Context(), "E1")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"handler": "E1", "show_advanced_options": true}; !reflect.DeepEqual(f.initBody, want) {
		t.Errorf("init body = %v", f.initBody)
	}
	if want := []string{"f1"}; !reflect.DeepEqual(f.deleted, want) {
		t.Errorf("deleted = %v, want the flow aborted", f.deleted)
	}
	if len(f.submitted) != 0 {
		t.Errorf("submitted = %v", f.submitted)
	}
	got, _ := json.Marshal(CurrentValues(form.Fields))
	if want := `{"entity_id":"sensor.source","hysteresis":0.5,"upper":10.0}`; string(got) != want {
		t.Errorf("current values = %s, want %s", got, want)
	}
}

func TestOptionsFormAbortIsError(t *testing.T) {
	f := &fakeFlow{t: t, path: optionsPath, init: `{"type":"abort","flow_id":"f1","reason":"not_loaded"}`}

	_, err := NewOptionsFlows(serve(t, f.handle)).Form(t.Context(), "E1")

	var aborted *FlowAbortedError
	if !errors.As(err, &aborted) || aborted.Reason != "not_loaded" {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionsFormFailedAbortIsError(t *testing.T) {
	rest := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(thresholdForm))
	})

	_, err := NewOptionsFlows(rest).Form(t.Context(), "E1")

	if err == nil || !strings.Contains(err.Error(), "aborting flow f1") {
		t.Fatalf("err = %v, want the failed abort", err)
	}
}

func TestOptionsSetKeepsOtherFields(t *testing.T) {
	f := &fakeFlow{
		t: t, path: optionsPath, init: thresholdForm,
		replies: []string{`{"type":"create_entry","flow_id":"f1","handler":"E1","result":true}`},
	}

	err := NewOptionsFlows(serve(t, f.handle)).Set(t.Context(), "E1", map[string]map[string]any{
		"init": {"upper": json.Number("20")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"entity_id": "sensor.source", "hysteresis": 0.5, "upper": float64(20)}}
	if !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v\nwant      %v", f.submitted, want)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestOptionsAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		form  string
		steps map[string]map[string]any
		want  string
	}{
		"no step keeps everything": {
			form: thresholdForm,
			want: `{"entity_id":"sensor.source","hysteresis":0.5,"upper":10.0}`,
		},
		"null unsets a field": {
			form:  thresholdForm,
			steps: map[string]map[string]any{"init": {"upper": nil, "lower": json.Number("5")}},
			want:  `{"entity_id":"sensor.source","hysteresis":0.5,"lower":5}`,
		},
		"section merged field by field": {
			form:  sectionForm,
			steps: map[string]map[string]any{"init": {"advanced": map[string]any{"retries": json.Number("5")}}},
			want:  `{"advanced":{"interval":30,"retries":5}}`,
		},
		"other step ignored": {
			form:  sectionForm,
			steps: map[string]map[string]any{"other": {"name": "x"}},
			want:  `{"advanced":{"interval":30,"retries":3}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := OptionsAnswer(tc.steps)(t.Context(), decodeForm(t, tc.form))
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := json.Marshal(data); string(got) != tc.want {
				t.Errorf("answer = %s\nwant     %s", got, tc.want)
			}
		})
	}
}

func TestOptionsAnswerUnknownField(t *testing.T) {
	answer := OptionsAnswer(map[string]map[string]any{
		"init": {"upper": 1, "uper": 2, "advanced": map[string]any{"x": 1}},
	})

	_, err := answer(t.Context(), decodeForm(t, thresholdForm))

	var unknown *FlowUnknownFieldsError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v", err)
	}
	want := `step "init" of options flow has no field "advanced", "uper"; its fields are "entity_id", "hysteresis", "lower", "upper"`
	if err.Error() != want {
		t.Errorf("err = %v\nwant  %s", err, want)
	}
}

func TestOptionsAnswerUnknownFieldInSection(t *testing.T) {
	err := decodeForm(t, sectionForm).CheckFields(map[string]any{"advanced": map[string]any{"retry": 1}})

	var unknown *FlowUnknownFieldsError
	if !errors.As(err, &unknown) || !reflect.DeepEqual(unknown.Fields, []string{"advanced.retry"}) {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionsAnswerSameStepTwiceFails(t *testing.T) {
	answer := OptionsAnswer(nil)
	form := decodeForm(t, thresholdForm)
	if _, err := answer(t.Context(), form); err != nil {
		t.Fatal(err)
	}

	_, err := answer(t.Context(), form)

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || !missing.Repeated {
		t.Fatalf("err = %v", err)
	}
}

func TestReadFields(t *testing.T) {
	form := decodeForm(t, sectionForm)

	got := ReadFields(form.Fields, map[string]any{
		"name":     "old",
		"advanced": map[string]any{"retries": json.Number("1")},
		"gone":     "kept",
	})

	// name has no value in HA, retries reads its suggested value, and a field the form lacks
	// cannot be read.
	if js, _ := json.Marshal(got); string(js) != `{"advanced":{"retries":3},"gone":"kept","name":null}` {
		t.Errorf("read = %s", js)
	}
}
