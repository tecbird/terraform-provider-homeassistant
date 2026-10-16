package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeFlow is a fake config-flow endpoint. init answers the POST that starts the flow; each later
// POST to the flow is recorded and answered by the next entry of replies.
type fakeFlow struct {
	t       *testing.T
	init    string
	replies []string
	// path is the flow endpoint below /api; it defaults to the config-flow one.
	path string

	mu        sync.Mutex
	initBody  map[string]any
	submitted []map[string]any
	deleted   []string
	// uploads are the contents of the files posted to /api/file_upload, in order.
	uploads []string
}

func (f *fakeFlow) client() ConfigFlows {
	return NewConfigFlows(serve(f.t, f.handle))
}

func (f *fakeFlow) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	base := "/api/" + f.path
	if f.path == "" {
		base = "/api/config/config_entries/flow"
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/file_upload":
		file, _, err := r.FormFile("file")
		if err != nil {
			f.t.Errorf("upload: %v", err)
			return
		}
		b, _ := io.ReadAll(file)
		f.uploads = append(f.uploads, string(b))
		_, _ = fmt.Fprintf(w, `{"file_id":"file%d"}`, len(f.uploads))
	case r.Method == http.MethodPost && r.URL.Path == base:
		f.initBody = decodeBody(f.t, r)
		_, _ = w.Write([]byte(f.init))
	case r.Method == http.MethodPost && r.URL.Path == base+"/f1":
		f.submitted = append(f.submitted, decodeBody(f.t, r))
		if len(f.replies) == 0 {
			f.t.Errorf("unexpected submit %v", f.submitted[len(f.submitted)-1])
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		reply := f.replies[0]
		f.replies = f.replies[1:]
		if code, body, ok := strings.Cut(reply, " "); ok && code == "400" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(reply))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, base+"/"))
		_, _ = w.Write([]byte(`{"message":"Flow aborted"}`))
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Errorf("decoding body: %v", err)
	}
	return m
}

const (
	formUser    = `{"type":"form","flow_id":"f1","handler":"shelly","step_id":"user","errors":{}}`
	formCreds   = `{"type":"form","flow_id":"f1","handler":"shelly","step_id":"credentials","errors":null}`
	createEntry = `{"type":"create_entry","flow_id":"f1","handler":"shelly","result":{"entry_id":"E1","domain":"shelly"}}`
)

func TestConfigFlowSingleStep(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{createEntry}}

	id, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{
		{StepID: "user", Data: map[string]any{"host": "192.168.1.50", "port": 80}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "E1" {
		t.Errorf("entry_id = %q", id)
	}
	if want := map[string]any{"handler": "shelly", "show_advanced_options": true}; !reflect.DeepEqual(f.initBody, want) {
		t.Errorf("init body = %v", f.initBody)
	}
	if want := []map[string]any{{"host": "192.168.1.50", "port": float64(80)}}; !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestConfigFlowMultipleStepsMergesSensitiveData(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formCreds, createEntry}}

	// Steps are matched by step_id, not by position.
	id, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{
		{
			StepID:        "credentials",
			Data:          map[string]any{"username": "admin", "password": "overridden"},
			SensitiveData: map[string]any{"password": "hunter2"},
		},
		{StepID: "user", Data: map[string]any{"host": "h"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "E1" {
		t.Errorf("entry_id = %q", id)
	}
	want := []map[string]any{
		{"host": "h"},
		{"username": "admin", "password": "hunter2"},
	}
	if !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
}

func TestConfigFlowRepeatedStepAnsweredInOrder(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formUser, formCreds, createEntry}}

	// Several FlowSteps with one step_id answer that form's visits in the order given.
	id, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{
		{StepID: "user", Data: map[string]any{"backend_type": "ollama"}},
		{StepID: "credentials", Data: map[string]any{"username": "admin"}},
		{StepID: "user", Data: map[string]any{"host": "h"}, SensitiveData: map[string]any{"api_key": "k"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "E1" {
		t.Errorf("entry_id = %q", id)
	}
	want := []map[string]any{
		{"backend_type": "ollama"},
		{"host": "h", "api_key": "k"},
		{"username": "admin"},
	}
	if !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v, want %v", f.submitted, want)
	}
	if len(f.deleted) != 0 {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestConfigFlowRepeatedStepWithoutSecondAnswerAborts(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formUser}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || missing.StepID != "user" || !missing.Repeated {
		t.Fatalf("err = %v", err)
	}
	if len(f.submitted) != 1 || !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("submitted = %v, deleted = %v", f.submitted, f.deleted)
	}
}

func TestConfigFlowRepeatedStepListExhaustedAborts(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formUser, formUser}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{
		{StepID: "user", Data: map[string]any{"n": 1}},
		{StepID: "user", Data: map[string]any{"n": 2}},
	})

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || missing.StepID != "user" || !missing.Repeated {
		t.Fatalf("err = %v", err)
	}
	want := []map[string]any{{"n": float64(1)}, {"n": float64(2)}}
	if !reflect.DeepEqual(f.submitted, want) || !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("submitted = %v, deleted = %v", f.submitted, f.deleted)
	}
}

func TestConfigFlowUnexpectedStepAborts(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formCreds}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || missing.StepID != "credentials" || missing.Repeated {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `asked for step "credentials", but the config only has "user"`) {
		t.Errorf("message = %q", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestConfigFlowRepeatedStepAborts(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{formUser}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || !missing.Repeated {
		t.Fatalf("err = %v", err)
	}
	if len(f.submitted) != 1 || !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("submitted = %v, deleted = %v", f.submitted, f.deleted)
	}
}

func TestConfigFlowFormErrorsAbort(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser, replies: []string{
		`{"type":"form","flow_id":"f1","step_id":"user","errors":{"base":"cannot_connect","host":"invalid_host"}}`,
	}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

	var formErr *FlowFormError
	if !errors.As(err, &formErr) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `rejected step "user": base: cannot_connect, host: invalid_host`) {
		t.Errorf("message = %q", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

// Ctrl-C cancels the context while a form is being answered: the flow is still aborted.
func TestConfigFlowAbortsWithCancelledContext(t *testing.T) {
	f := &fakeFlow{t: t, init: formUser}
	ctx, cancel := context.WithCancel(t.Context())

	_, err := f.client().RunAnswer(ctx, "shelly", "", func(context.Context, FlowForm) (map[string]any, error) {
		cancel()
		return nil, context.Canceled
	})

	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "aborting flow") {
		t.Fatalf("err = %v", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestConfigFlowRejectedSubmitAborts(t *testing.T) {
	// HA answers 400 when the data does not match the step's schema, and leaves the flow open.
	f := &fakeFlow{t: t, init: formUser, replies: []string{`400 {"message":"User input malformed: required key not provided @ data['host']"}`}}

	_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "required key not provided") {
		t.Errorf("message = %q", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestConfigFlowAbortReason(t *testing.T) {
	for name, tc := range map[string]struct {
		init    string
		replies []string
	}{
		"at init":     {init: `{"type":"abort","flow_id":"f1","reason":"already_configured"}`},
		"after start": {init: formUser, replies: []string{`{"type":"abort","flow_id":"f1","reason":"already_configured"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeFlow{t: t, init: tc.init, replies: tc.replies}

			_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

			var aborted *FlowAbortedError
			if !errors.As(err, &aborted) || aborted.Reason != "already_configured" {
				t.Fatalf("err = %v", err)
			}
			// HA already ended the flow; there is nothing to delete.
			if len(f.deleted) != 0 {
				t.Errorf("deleted = %v", f.deleted)
			}
		})
	}
}

func TestConfigFlowReconfigure(t *testing.T) {
	f := &fakeFlow{t: t, init: `{"type":"form","flow_id":"f1","step_id":"reconfigure","errors":{}}`, replies: []string{
		`{"type":"abort","flow_id":"f1","reason":"reconfigure_successful"}`,
	}}

	id, err := f.client().Run(t.Context(), "esphome", "E7", []FlowStep{
		{StepID: "reconfigure", Data: map[string]any{"host": "new"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "E7" {
		t.Errorf("entry_id = %q", id)
	}
	if want := map[string]any{"handler": "esphome", "show_advanced_options": true, "entry_id": "E7"}; !reflect.DeepEqual(f.initBody, want) {
		t.Errorf("init body = %v", f.initBody)
	}
}

func TestConfigFlowReconfigureSuccessfulOnlyCountsForReconfigure(t *testing.T) {
	f := &fakeFlow{t: t, init: `{"type":"abort","flow_id":"f1","reason":"reconfigure_successful"}`}

	_, err := f.client().Run(t.Context(), "esphome", "", nil)

	var aborted *FlowAbortedError
	if !errors.As(err, &aborted) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigFlowUnsupportedResultAborts(t *testing.T) {
	for _, typ := range []string{"menu", "external", "show_progress", "progress_done"} {
		t.Run(typ, func(t *testing.T) {
			f := &fakeFlow{t: t, init: formUser, replies: []string{
				`{"type":"` + typ + `","flow_id":"f1","step_id":"pick"}`,
			}}

			_, err := f.client().Run(t.Context(), "shelly", "", []FlowStep{{StepID: "user"}})

			var unsupported *FlowUnsupportedError
			if !errors.As(err, &unsupported) || unsupported.Type != typ {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
				t.Errorf("deleted = %v", f.deleted)
			}
		})
	}
}

func TestConfigFlowUnknownHandler(t *testing.T) {
	c := NewConfigFlows(serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Invalid handler specified"}`))
	}))

	_, err := c.Run(t.Context(), "nope", "", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigFlowAbortFailureIsReported(t *testing.T) {
	c := NewConfigFlows(serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"type":"menu","flow_id":"f1","step_id":"pick"}`))
	}))

	_, err := c.Run(t.Context(), "shelly", "", nil)

	var unsupported *FlowUnsupportedError
	if !errors.As(err, &unsupported) || !strings.Contains(err.Error(), "aborting flow f1") {
		t.Fatalf("err = %v", err)
	}
}

// MQTT's broker form before 2026.9: the advanced fields appear after a post with advanced_options,
// and the certificate field after a post that selects a custom CA.
const (
	mqttBasic = `{"type":"form","flow_id":"f1","step_id":"broker","errors":{},"data_schema":[
		{"name":"broker","required":true,"default":null},{"name":"port","required":true,"default":1883},
		{"name":"username","optional":true},{"name":"password","optional":true},
		{"name":"advanced_options","optional":true}]}`
	mqttAdvanced = `{"type":"form","flow_id":"f1","step_id":"broker","errors":{},"data_schema":[
		{"name":"broker","required":true,"default":"b"},{"name":"port","required":true,"default":1883},
		{"name":"keepalive","optional":true},{"name":"set_ca_cert","optional":true,"default":"off"}]}`
	mqttCert = `{"type":"form","flow_id":"f1","step_id":"broker","errors":{},"data_schema":[
		{"name":"broker","required":true,"default":"b"},{"name":"port","required":true,"default":1883},
		{"name":"keepalive","optional":true},{"name":"set_ca_cert","optional":true,"default":"custom"},
		{"name":"certificate","optional":true,"selector":{"file":{}}}]}`
	// MQTT's broker form since 2026.9: one form, with a section.
	mqttSection = `{"type":"form","flow_id":"f1","step_id":"broker","errors":{},"data_schema":[
		{"name":"broker","required":true},{"name":"port","required":true,"default":1883},
		{"name":"other_settings","required":true,"type":"expandable","expanded":false,"schema":[
			{"name":"keepalive","optional":true},{"name":"set_ca_cert","required":true},
			{"name":"certificate","optional":true,"selector":{"file":{}}}]}]}`
)

func TestFieldAnswerFollowsChangingForms(t *testing.T) {
	f := &fakeFlow{t: t, init: mqttBasic, replies: []string{mqttAdvanced, mqttCert, createEntry}}
	rest := serve(t, f.handle)
	values := map[string]any{"broker": "b", "port": 8883, "keepalive": 30, "set_ca_cert": "custom"}
	files := map[string]string{"certificate": "PEM"}
	answer := FieldAnswer(rest, values, files)
	// Stand-in for the MQTT resource, which asks for the advanced fields.
	expand := func(ctx context.Context, form FlowForm) (map[string]any, error) {
		data, err := answer(ctx, form)
		if err == nil && form.Has("advanced_options") {
			data["advanced_options"] = true
		}
		return data, err
	}

	id, err := NewConfigFlows(rest).RunAnswer(t.Context(), "mqtt", "", expand)
	if err != nil {
		t.Fatal(err)
	}
	if id != "E1" {
		t.Errorf("entry_id = %q", id)
	}
	want := []map[string]any{
		{"broker": "b", "port": float64(8883), "advanced_options": true},
		{"broker": "b", "port": float64(8883), "keepalive": float64(30), "set_ca_cert": "custom"},
		{"broker": "b", "port": float64(8883), "keepalive": float64(30), "set_ca_cert": "custom", "certificate": "file1"},
	}
	if !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
	if !reflect.DeepEqual(f.uploads, []string{"PEM"}) {
		t.Errorf("uploads = %v", f.uploads)
	}
}

func TestFieldAnswerFillsSections(t *testing.T) {
	f := &fakeFlow{t: t, init: mqttSection, replies: []string{createEntry}}
	rest := serve(t, f.handle)
	values := map[string]any{"broker": "b", "set_ca_cert": "auto", "unused": true}

	if _, err := NewConfigFlows(rest).RunAnswer(t.Context(), "mqtt", "", FieldAnswer(rest, values, nil)); err != nil {
		t.Fatal(err)
	}
	// port and keepalive are left to HA's defaults; the section is a nested object.
	want := []map[string]any{{"broker": "b", "other_settings": map[string]any{"set_ca_cert": "auto"}}}
	if !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
}

func TestFieldAnswerRequiredFieldMissing(t *testing.T) {
	f := &fakeFlow{t: t, init: mqttSection}
	rest := serve(t, f.handle)

	_, err := NewConfigFlows(rest).RunAnswer(t.Context(), "mqtt", "", FieldAnswer(rest, map[string]any{"broker": "b"}, nil))

	var missing *FlowFieldMissingError
	if !errors.As(err, &missing) || missing.Field != "set_ca_cert" {
		t.Fatalf("err = %v", err)
	}
	if want := `config flow for mqtt asked for "set_ca_cert" in step "broker", which is not set`; err.Error() != want {
		t.Errorf("message = %q", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestFieldAnswerSameFormTwiceAborts(t *testing.T) {
	f := &fakeFlow{t: t, init: mqttAdvanced, replies: []string{mqttAdvanced}}
	rest := serve(t, f.handle)

	_, err := NewConfigFlows(rest).RunAnswer(t.Context(), "mqtt", "", FieldAnswer(rest, map[string]any{"broker": "b"}, nil))

	var missing *FlowStepMissingError
	if !errors.As(err, &missing) || !missing.Repeated {
		t.Fatalf("err = %v", err)
	}
	if len(f.submitted) != 1 || !reflect.DeepEqual(f.deleted, []string{"f1"}) {
		t.Errorf("submitted = %v, deleted = %v", f.submitted, f.deleted)
	}
}

func TestFieldAnswerFailedUploadAborts(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/file_upload":
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		case r.Method == http.MethodDelete:
		default:
			_, _ = w.Write([]byte(mqttCert))
		}
	})

	_, err := NewConfigFlows(c).RunAnswer(t.Context(), "mqtt", "", FieldAnswer(c, nil, map[string]string{"certificate": "PEM"}))

	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || !strings.Contains(err.Error(), `uploading certificate for step "broker"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestOptionsFlow(t *testing.T) {
	f := &fakeFlow{
		t: t, path: "config/config_entries/options/flow", init: `{"type":"form","flow_id":"f1","step_id":"init","data_schema":[
		{"name":"allow_service_calls","required":true,"default":false},{"name":"subscribe_logs","required":true,"default":false}]}`,
		replies: []string{`{"type":"create_entry","flow_id":"f1","handler":"E1","result":true}`},
	}
	rest := serve(t, f.handle)

	err := NewOptionsFlows(rest).Run(t.Context(), "E1", FieldAnswer(rest, map[string]any{"allow_service_calls": true}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"handler": "E1"}; !reflect.DeepEqual(f.initBody, want) {
		t.Errorf("init body = %v", f.initBody)
	}
	if want := []map[string]any{{"allow_service_calls": true}}; !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
}

func TestOptionsFlowAbortIsError(t *testing.T) {
	f := &fakeFlow{t: t, path: "config/config_entries/options/flow", init: `{"type":"abort","flow_id":"f1","reason":"not_loaded"}`}
	rest := serve(t, f.handle)

	err := NewOptionsFlows(rest).Run(t.Context(), "E1", FieldAnswer(rest, nil, nil))

	var aborted *FlowAbortedError
	if !errors.As(err, &aborted) || err.Error() != "options flow of config entry E1 aborted: not_loaded" {
		t.Fatalf("err = %v", err)
	}
}
