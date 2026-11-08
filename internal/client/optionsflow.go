package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// errFormRead ends the flow that Form starts, once it has the first form.
var errFormRead = errors.New("reading the current options")

// optionsInit starts an options flow of entryID. Advanced fields are shown, as for config flows.
func optionsInit(entryID string) map[string]any {
	return map[string]any{"handler": entryID, "show_advanced_options": true}
}

// Form starts the options flow of the config entry, returns its first form, and aborts the flow
// (`DELETE`), so nothing changes. A menu is unsupported (FlowUnsupportedError). The form's suggested values are the current options
// (spec/integrations.md, "homeassistant_integration_options").
func (f OptionsFlows) Form(ctx context.Context, entryID string) (FlowForm, error) {
	name := "options flow of config entry " + entryID
	var form FlowForm
	res, err := f.driver.run(ctx, name, optionsInit(entryID), func(_ context.Context, ff FlowForm) (map[string]any, error) {
		if ff.Menu {
			return nil, &FlowUnsupportedError{Flow: ff.Flow, Type: "menu", StepID: ff.StepID}
		}
		form = ff
		return nil, errFormRead
	})
	if err == errFormRead { //nolint:errorlint // only the bare sentinel means the abort succeeded
		return form, nil
	}
	if err != nil {
		return FlowForm{}, err
	}
	// The flow ended without showing a form.
	if res.Type == "abort" {
		return FlowForm{}, &FlowAbortedError{Flow: name, Reason: res.Reason}
	}
	return FlowForm{}, fmt.Errorf("%s ended without showing a form", name)
}

// Set runs the options flow of the config entry. Each form is answered with its current values,
// overlaid with the fields of the step of the same step_id (OptionsAnswer). Any abort is an error,
// and so is a step whose step_id the flow never showed: its fields were not set.
func (f OptionsFlows) Set(ctx context.Context, entryID string, steps map[string]map[string]any) error {
	name := "options flow of config entry " + entryID
	shown := map[string]bool{}
	answer := OptionsAnswer(steps)
	res, err := f.driver.run(ctx, name, optionsInit(entryID), func(ctx context.Context, form FlowForm) (map[string]any, error) {
		shown[form.StepID] = true
		return answer(ctx, form)
	})
	if err != nil {
		return err
	}
	if res.Type == "abort" {
		return &FlowAbortedError{Flow: name, Reason: res.Reason}
	}
	var notShown []string
	for id := range steps {
		if !shown[id] {
			notShown = append(notShown, id)
		}
	}
	if len(notShown) > 0 {
		slices.Sort(notShown)
		return fmt.Errorf("%s ended without showing step %s, so its fields were not set; it showed %s",
			name, quoteList(notShown), quoteList(slices.Sorted(maps.Keys(shown))))
	}
	return nil
}

// OptionsAnswer answers each form of an options flow as the UI does when only some fields are
// edited: every field keeps its current value (CurrentValues), and the fields of the step with
// the form's step_id replace it. A field set to nil is left out, which unsets an optional option;
// a section is merged field by field. A form whose step_id has no step is answered with its
// current values. The same step_id a second time is an error, so the flow cannot loop. A menu is
// unsupported: its choice is not an option the flow keeps.
func OptionsAnswer(steps map[string]map[string]any) FlowAnswer {
	seen := map[string]bool{}
	return func(_ context.Context, form FlowForm) (map[string]any, error) {
		if form.Menu {
			return nil, &FlowUnsupportedError{Flow: form.Flow, Type: "menu", StepID: form.StepID}
		}
		if seen[form.StepID] {
			return nil, &FlowStepMissingError{Flow: form.Flow, StepID: form.StepID, Repeated: true}
		}
		seen[form.StepID] = true
		if err := form.CheckFields(steps[form.StepID]); err != nil {
			return nil, err
		}
		return overlay(form.Fields, CurrentValues(form.Fields), steps[form.StepID]), nil
	}
}

// overlay returns current with the fields of set replacing it; a nil value removes the field.
func overlay(fields []FormField, current, set map[string]any) map[string]any {
	out := maps.Clone(current)
	if out == nil {
		out = map[string]any{}
	}
	for _, f := range fields {
		v, ok := set[f.Name]
		switch {
		case !ok:
		case v == nil:
			delete(out, f.Name)
		case f.Type == "expandable":
			sub, _ := v.(map[string]any)
			cur, _ := out[f.Name].(map[string]any)
			out[f.Name] = overlay(f.Schema, cur, sub)
		default:
			out[f.Name] = v
		}
	}
	return out
}

// CurrentValues returns the value each field of a form shows before the user edits it, as the
// frontend fills the form: the suggested value, else the default. Fields with neither, or with a
// null one, are left out. A section is an object of its own fields.
func CurrentValues(fields []FormField) map[string]any {
	out := map[string]any{}
	for _, f := range fields {
		if f.Type == "expandable" {
			out[f.Name] = CurrentValues(f.Schema)
			continue
		}
		if v, ok := f.current(); ok {
			out[f.Name] = v
		}
	}
	return out
}

// current returns the value the field shows: the suggested value, else the default.
func (f FormField) current() (any, bool) {
	if f.Description != nil {
		if v := decodeValue(f.Description.SuggestedValue); v != nil {
			return v, true
		}
	}
	if v := decodeValue(f.Default); v != nil {
		return v, true
	}
	return nil, false
}

// decodeValue decodes a JSON value, keeping numbers exact; absent or invalid JSON is nil.
func decodeValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	return v
}

// ReadFields returns the current value of each field in set, by the form's fields: a field
// without a current value is nil, a section an object of the fields set in it. A field the form
// does not have keeps its value from set, because it cannot be read.
func ReadFields(fields []FormField, set map[string]any) map[string]any {
	byName := make(map[string]FormField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	out := make(map[string]any, len(set))
	for name, v := range set {
		f, ok := byName[name]
		switch {
		case !ok:
			out[name] = v
		case f.Type == "expandable":
			sub, isObject := v.(map[string]any)
			if !isObject {
				sub = map[string]any{}
			}
			out[name] = ReadFields(f.Schema, sub)
		default:
			out[name], _ = f.current()
		}
	}
	return out
}

// CheckFields returns a *FlowUnknownFieldsError if set has fields that the form does not have, and
// an error if it sets a section to something other than an object, null included.
func (f FlowForm) CheckFields(set map[string]any) error {
	if unknown := unknownFields(f.Fields, set); len(unknown) > 0 {
		return &FlowUnknownFieldsError{Flow: f.Flow, StepID: f.StepID, Fields: unknown, Known: fieldNames(f.Fields)}
	}
	if bad := nonObjectSections(f.Fields, set); len(bad) > 0 {
		return fmt.Errorf("step %q of %s sets section %s to a value that is not an object of its fields",
			f.StepID, f.Flow, quoteList(bad))
	}
	return nil
}

// nonObjectSections returns the names in set, sorted, of sections whose value is not an object
// (null included: it would read back as the section's current values), also inside sections, as
// "section.field".
func nonObjectSections(fields []FormField, set map[string]any) []string {
	var bad []string
	for _, f := range fields {
		v, ok := set[f.Name]
		if !ok || f.Type != "expandable" {
			continue
		}
		sub, isObject := v.(map[string]any)
		if !isObject {
			bad = append(bad, f.Name)
			continue
		}
		for _, b := range nonObjectSections(f.Schema, sub) {
			bad = append(bad, f.Name+"."+b)
		}
	}
	slices.Sort(bad)
	return bad
}

// unknownFields returns the names in set, sorted, that are not fields of the form, also inside
// sections, as "section.field".
func unknownFields(fields []FormField, set map[string]any) []string {
	byName := make(map[string]FormField, len(fields))
	for _, f := range fields {
		byName[f.Name] = f
	}
	var unknown []string
	for name, v := range set {
		f, ok := byName[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if sub, isObject := v.(map[string]any); isObject && f.Type == "expandable" {
			for _, u := range unknownFields(f.Schema, sub) {
				unknown = append(unknown, name+"."+u)
			}
		}
	}
	slices.Sort(unknown)
	return unknown
}

// FlowUnknownFieldsError means the config sets fields that a form does not have.
type FlowUnknownFieldsError struct {
	Flow   string
	StepID string
	Fields []string
	// Known are the fields the form has.
	Known []string
}

func (e *FlowUnknownFieldsError) Error() string {
	known := slices.Clone(e.Known)
	slices.Sort(known)
	return fmt.Sprintf("step %q of %s has no field %s; its fields are %s",
		e.StepID, e.Flow, quoteList(e.Fields), quoteList(known))
}
