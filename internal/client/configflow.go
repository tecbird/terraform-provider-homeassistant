package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// FlowStep is the input for one form of a config flow, keyed by the form's step_id.
type FlowStep struct {
	StepID string
	Data   map[string]any
	// SensitiveData is merged over Data when the step is submitted (ADR-0014).
	SensitiveData map[string]any
}

// input returns the data submitted for the step: Data ∪ SensitiveData, SensitiveData winning on a
// shared key.
func (s FlowStep) input() map[string]any {
	in := make(map[string]any, len(s.Data)+len(s.SensitiveData))
	maps.Copy(in, s.Data)
	maps.Copy(in, s.SensitiveData)
	return in
}

// FlowForm is a form that a flow shows.
type FlowForm struct {
	// Flow names the flow for error messages, e.g. "config flow for shelly".
	Flow   string
	StepID string
	// Fields is the form's data_schema.
	Fields []FormField
}

// FormField is one field of a form's data_schema, as HA serializes it.
type FormField struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	// Type is "expandable" for a section, whose fields are in Schema.
	Type   string      `json:"type"`
	Schema []FormField `json:"schema"`
	// Default is absent (nil) if the field has no default.
	Default json.RawMessage `json:"default"`
	// Description holds the suggested value, which an options flow sets to the current option.
	Description *FieldDescription `json:"description"`
}

// FieldDescription is the description of a form field.
type FieldDescription struct {
	// SuggestedValue is absent (nil) if the field has none.
	SuggestedValue json.RawMessage `json:"suggested_value"`
}

// Has reports whether the form has a field with the given name, also inside a section.
func (f FlowForm) Has(name string) bool {
	return slices.Contains(fieldNames(f.Fields), name)
}

// fieldNames returns the names of all fields, with those of sections in place of the section.
func fieldNames(fields []FormField) []string {
	var names []string
	for _, f := range fields {
		if f.Type == "expandable" {
			names = append(names, fieldNames(f.Schema)...)
			continue
		}
		names = append(names, f.Name)
	}
	return names
}

// FlowAnswer returns the data that answers a form.
type FlowAnswer func(ctx context.Context, form FlowForm) (map[string]any, error)

// ReasonReconfigureSuccessful is the abort reason that ends a successful reconfigure flow.
const ReasonReconfigureSuccessful = "reconfigure_successful"

// FlowAbortedError means Home Assistant aborted a flow, e.g. `already_configured`.
type FlowAbortedError struct {
	Flow   string
	Reason string
}

func (e *FlowAbortedError) Error() string {
	return fmt.Sprintf("%s aborted: %s", e.Flow, e.Reason)
}

// FlowFormError means Home Assistant rejected the data of a step, e.g. `{"base": "cannot_connect"}`.
type FlowFormError struct {
	Flow   string
	StepID string
	Errors map[string]string
}

func (e *FlowFormError) Error() string {
	fields := slices.Sorted(maps.Keys(e.Errors))
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f + ": " + e.Errors[f]
	}
	return fmt.Sprintf("%s rejected step %q: %s", e.Flow, e.StepID, strings.Join(parts, ", "))
}

// FlowStepMissingError means Home Assistant asked for a step the config does not provide.
type FlowStepMissingError struct {
	Flow   string
	StepID string
	// Provided are the step_ids the config provides.
	Provided []string
	// Repeated means the step was answered but HA showed the same form a second time.
	Repeated bool
}

func (e *FlowStepMissingError) Error() string {
	if e.Repeated {
		return fmt.Sprintf("%s asked for step %q a second time", e.Flow, e.StepID)
	}
	return fmt.Sprintf("%s asked for step %q, but the config only has %s",
		e.Flow, e.StepID, quoteList(e.Provided))
}

// FlowFieldMissingError means a form requires a field that has no default and no value.
type FlowFieldMissingError struct {
	Flow   string
	StepID string
	Field  string
}

func (e *FlowFieldMissingError) Error() string {
	return fmt.Sprintf("%s asked for %q in step %q, which is not set", e.Flow, e.Field, e.StepID)
}

// FlowUnsupportedError means the flow returned a result the driver cannot handle, e.g. a menu,
// an external step, or a progress step.
type FlowUnsupportedError struct {
	Flow   string
	Type   string
	StepID string
}

func (e *FlowUnsupportedError) Error() string {
	return fmt.Sprintf("%s returned unsupported result %q (step %q)", e.Flow, e.Type, e.StepID)
}

// flowResult is the subset of a FlowResult the driver reads.
type flowResult struct {
	Type       string            `json:"type"`
	FlowID     string            `json:"flow_id"`
	StepID     string            `json:"step_id"`
	DataSchema []FormField       `json:"data_schema"`
	Errors     map[string]string `json:"errors"`
	Reason     string            `json:"reason"`
	// Result is the config entry of a config flow's create_entry; an options flow has `true`.
	Result json.RawMessage `json:"result"`
}

// flowDriver drives the flows at one REST path: it answers each form until HA ends the flow.
type flowDriver struct {
	rest *RESTClient
	path string
}

// run starts the flow with init and answers each form. It returns the result that ends the flow:
// create_entry or abort.
//
// Form errors, a failed answer, a rejected submit, or an unsupported result abort the flow
// (`DELETE`) before the error is returned. The DELETE runs even if ctx is cancelled.
func (d flowDriver) run(ctx context.Context, name string, init map[string]any, answer FlowAnswer) (flowResult, error) {
	var res flowResult
	if err := d.rest.Do(ctx, http.MethodPost, d.path, init, &res); err != nil {
		return res, fmt.Errorf("starting %s: %w", name, err)
	}
	for {
		switch res.Type {
		case "create_entry", "abort":
			return res, nil

		case "form":
			if len(res.Errors) > 0 {
				return res, d.abort(ctx, res.FlowID, &FlowFormError{Flow: name, StepID: res.StepID, Errors: res.Errors})
			}
			data, err := answer(ctx, FlowForm{Flow: name, StepID: res.StepID, Fields: res.DataSchema})
			if err != nil {
				return res, d.abort(ctx, res.FlowID, err)
			}
			flowID, stepID := res.FlowID, res.StepID
			res = flowResult{}
			if err := d.rest.Do(ctx, http.MethodPost, d.path+"/"+url.PathEscape(flowID), data, &res); err != nil {
				return res, d.abort(ctx, flowID, fmt.Errorf("submitting step %q of %s: %w", stepID, name, err))
			}

		default:
			return res, d.abort(ctx, res.FlowID, &FlowUnsupportedError{Flow: name, Type: res.Type, StepID: res.StepID})
		}
	}
}

// abort deletes the flow and returns cause, joined with the abort's own failure if any.
func (d flowDriver) abort(ctx context.Context, flowID string, cause error) error {
	if flowID == "" {
		return cause
	}
	err := d.rest.Do(context.WithoutCancel(ctx), http.MethodDelete, d.path+"/"+url.PathEscape(flowID), nil, nil)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("aborting flow %s: %w", flowID, err))
	}
	return cause
}

// ConfigFlows drives config flows over REST (`/api/config/config_entries/flow`, ADR-0004).
type ConfigFlows struct {
	driver flowDriver
}

// NewConfigFlows returns the config-flow API on rest.
func NewConfigFlows(rest *RESTClient) ConfigFlows {
	return ConfigFlows{driver: flowDriver{rest: rest, path: "config/config_entries/flow"}}
}

// Run starts a config flow for handler, answers each form with the step of the same step_id, and
// returns the entry_id of the config entry.
//
// A non-empty entryID starts a reconfigure flow for that entry instead; it returns entryID once HA
// aborts with ReasonReconfigureSuccessful.
//
// A missing step, form errors, or an unsupported result abort the flow (`DELETE`) before the error
// is returned. Each step is submitted at most once, so a flow that asks for a step again fails
// instead of looping.
func (f ConfigFlows) Run(ctx context.Context, handler, entryID string, steps []FlowStep) (string, error) {
	byID := make(map[string]FlowStep, len(steps))
	for _, s := range steps {
		byID[s.StepID] = s
	}
	submitted := map[string]bool{}
	return f.RunAnswer(ctx, handler, entryID, func(_ context.Context, form FlowForm) (map[string]any, error) {
		step, ok := byID[form.StepID]
		if !ok || submitted[form.StepID] {
			return nil, &FlowStepMissingError{
				Flow: form.Flow, StepID: form.StepID, Provided: slices.Sorted(maps.Keys(byID)), Repeated: ok,
			}
		}
		submitted[form.StepID] = true
		return step.input(), nil
	})
}

// RunAnswer is Run with each form answered by answer instead of by a step.
func (f ConfigFlows) RunAnswer(ctx context.Context, handler, entryID string, answer FlowAnswer) (string, error) {
	name := "config flow for " + handler
	init := map[string]any{"handler": handler, "show_advanced_options": true}
	if entryID != "" {
		name = "reconfigure flow for " + handler
		init["entry_id"] = entryID
	}
	res, err := f.driver.run(ctx, name, init, answer)
	if err != nil {
		return "", err
	}
	if res.Type == "abort" {
		if entryID != "" && res.Reason == ReasonReconfigureSuccessful {
			return entryID, nil
		}
		return "", &FlowAbortedError{Flow: name, Reason: res.Reason}
	}
	var entry struct {
		EntryID string `json:"entry_id"`
	}
	if len(res.Result) > 0 {
		_ = json.Unmarshal(res.Result, &entry)
	}
	if entry.EntryID == "" {
		return "", fmt.Errorf("%s created an entry without entry_id", name)
	}
	return entry.EntryID, nil
}

// OptionsFlows drives options flows over REST (`/api/config/config_entries/options/flow`).
type OptionsFlows struct {
	driver flowDriver
}

// NewOptionsFlows returns the options-flow API on rest.
func NewOptionsFlows(rest *RESTClient) OptionsFlows {
	return OptionsFlows{driver: flowDriver{rest: rest, path: "config/config_entries/options/flow"}}
}

// Run starts the options flow of the config entry and answers each form. Any abort is an error.
func (f OptionsFlows) Run(ctx context.Context, entryID string, answer FlowAnswer) error {
	name := "options flow of config entry " + entryID
	res, err := f.driver.run(ctx, name, map[string]any{"handler": entryID}, answer)
	if err != nil {
		return err
	}
	if res.Type == "abort" {
		return &FlowAbortedError{Flow: name, Reason: res.Reason}
	}
	return nil
}

// FieldAnswer answers every form from one pool of field values, by the form's data_schema, so the
// caller need not know which forms a flow shows (spec/integrations.md, "Answering forms from
// fields").
//
// A field present in values is answered with its value, and a field present in files with the
// file_id of the content, uploaded to rest. A section is answered with a nested object of its own
// fields. Other fields are left out, so HA applies its defaults; a required one without a default
// is an error. The same step with the same fields a second time is an error, so the flow cannot
// loop.
func FieldAnswer(rest *RESTClient, values map[string]any, files map[string]string) FlowAnswer {
	seen := map[string]bool{}
	return func(ctx context.Context, form FlowForm) (map[string]any, error) {
		names := fieldNames(form.Fields)
		slices.Sort(names)
		key := form.StepID + "\x00" + strings.Join(names, "\x00")
		if seen[key] {
			return nil, &FlowStepMissingError{Flow: form.Flow, StepID: form.StepID, Repeated: true}
		}
		seen[key] = true
		return fillFields(ctx, rest, form, form.Fields, values, files)
	}
}

func fillFields(ctx context.Context, rest *RESTClient, form FlowForm, fields []FormField,
	values map[string]any, files map[string]string,
) (map[string]any, error) {
	data := map[string]any{}
	for _, f := range fields {
		if f.Type == "expandable" {
			section, err := fillFields(ctx, rest, form, f.Schema, values, files)
			if err != nil {
				return nil, err
			}
			data[f.Name] = section
			continue
		}
		if v, ok := values[f.Name]; ok {
			data[f.Name] = v
			continue
		}
		if content, ok := files[f.Name]; ok {
			id, err := rest.Upload(ctx, f.Name+".pem", []byte(content))
			if err != nil {
				return nil, fmt.Errorf("uploading %s for step %q of %s: %w", f.Name, form.StepID, form.Flow, err)
			}
			data[f.Name] = id
			continue
		}
		if f.Required && f.Default == nil {
			return nil, &FlowFieldMissingError{Flow: form.Flow, StepID: form.StepID, Field: f.Name}
		}
	}
	return data, nil
}

func quoteList(ss []string) string {
	if len(ss) == 0 {
		return "none"
	}
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return strings.Join(q, ", ")
}
