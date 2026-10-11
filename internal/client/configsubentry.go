package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
)

// ConfigSubentry is the readable part of a config subentry. Like a config entry's, its input data
// cannot be read back (ADR-0014).
type ConfigSubentry struct {
	SubentryID   string `json:"subentry_id"`
	SubentryType string `json:"subentry_type"`
	Title        string `json:"title"`
	// UniqueID is nil if the integration set none.
	UniqueID *string `json:"unique_id"`
}

const (
	cmdSubentriesList   = "config_entries/subentries/list"
	cmdSubentriesDelete = "config_entries/subentries/delete"
	cmdEntryGetSingle   = "config_entries/get_single"
	subentryFlowPath    = "config/config_entries/subentries/flow"
)

// ConfigSubentries reads and deletes the subentries of config entries over WebSocket
// (`config_entries/subentries/list|delete`), and creates and reconfigures them through subentry
// flows over REST (`/api/config/config_entries/subentries/flow`, ADR-0004).
type ConfigSubentries struct {
	ws     WSCommander
	driver flowDriver
}

// NewConfigSubentries returns the config-subentry API on ws and rest.
func NewConfigSubentries(ws WSCommander, rest *RESTClient) ConfigSubentries {
	return ConfigSubentries{ws: ws, driver: flowDriver{rest: rest, path: subentryFlowPath}}
}

// List returns the subentries of the config entry. A missing config entry is ErrNotFound.
func (c ConfigSubentries) List(ctx context.Context, entryID string) ([]ConfigSubentry, error) {
	var subentries []ConfigSubentry
	err := c.ws.Command(ctx, cmdSubentriesList, map[string]any{"entry_id": entryID}, &subentries)
	if isWSNotFound(err) {
		return nil, fmt.Errorf("config entry %s: %w", entryID, ErrNotFound)
	}
	return subentries, err
}

// Get returns the subentry of the config entry. ok is false if there is none, also when the config
// entry itself is gone.
func (c ConfigSubentries) Get(ctx context.Context, entryID, subentryID string) (sub ConfigSubentry, ok bool, err error) {
	subentries, err := c.List(ctx, entryID)
	if errors.Is(err, ErrNotFound) {
		return sub, false, nil
	}
	if err != nil {
		return sub, false, err
	}
	for _, s := range subentries {
		if s.SubentryID == subentryID {
			return s, true, nil
		}
	}
	return sub, false, nil
}

// Delete deletes the subentry. A subentry or config entry that is already gone is not an error.
func (c ConfigSubentries) Delete(ctx context.Context, entryID, subentryID string) error {
	err := c.ws.Command(ctx, cmdSubentriesDelete, map[string]any{"entry_id": entryID, "subentry_id": subentryID}, nil)
	if isWSNotFound(err) {
		return nil
	}
	return err
}

// SubentryTypeUnknownError means HA answered the subentry flow with 404, which it does when the
// config entry does not offer subentries of the type.
type SubentryTypeUnknownError struct {
	EntryID      string
	SubentryType string
	// Err is the 404 as the flow driver reported it.
	Err error
}

func (e *SubentryTypeUnknownError) Error() string {
	return fmt.Sprintf("config entry %s has no subentry type %q (%v)", e.EntryID, e.SubentryType, e.Err)
}

func (e *SubentryTypeUnknownError) Unwrap() error { return e.Err }

// Create runs a subentry flow of subentryType for the config entry, answers each form with the step
// of the same step_id, as ConfigFlows.Run does, and returns the new subentry.
//
// HA's create_entry does not name the new subentry, so Create lists the subentries before and after
// the flow, and picks the new one of subentryType, if needed by the flow's title. Flows on the same
// config entry run one at a time within the provider, so they cannot mistake each other's
// subentries.
func (c ConfigSubentries) Create(ctx context.Context, entryID, subentryType string, steps []FlowStep) (ConfigSubentry, error) {
	defer subentryFlows.lock(entryID)()

	before, err := c.List(ctx, entryID)
	if err != nil {
		return ConfigSubentry{}, err
	}
	name := fmt.Sprintf("subentry flow for %s of config entry %s", subentryType, entryID)
	init := map[string]any{"handler": []string{entryID, subentryType}}
	res, err := c.driver.run(ctx, name, init, stepAnswer(steps))
	if errors.Is(err, ErrNotFound) {
		// The entry exists (List succeeded), and HA answers 404 to a start with a type the entry
		// does not offer.
		return ConfigSubentry{}, &SubentryTypeUnknownError{EntryID: entryID, SubentryType: subentryType, Err: err}
	}
	if err != nil {
		return ConfigSubentry{}, err
	}
	if res.Type == "abort" {
		return ConfigSubentry{}, &FlowAbortedError{Flow: name, Reason: res.Reason}
	}

	after, err := c.List(ctx, entryID)
	if err != nil {
		return ConfigSubentry{}, fmt.Errorf("reading the subentries of config entry %s after %s: %w", entryID, name, err)
	}
	return newSubentry(before, after, subentryType, res.Title)
}

// newSubentry returns the subentry of subentryType that is in after but not in before. If there are
// several, the one with the title is picked.
func newSubentry(before, after []ConfigSubentry, subentryType, title string) (ConfigSubentry, error) {
	var created []ConfigSubentry
	for _, s := range after {
		if s.SubentryType == subentryType && !slices.ContainsFunc(before, func(b ConfigSubentry) bool {
			return b.SubentryID == s.SubentryID
		}) {
			created = append(created, s)
		}
	}
	if len(created) > 1 {
		created = slices.DeleteFunc(created, func(s ConfigSubentry) bool { return s.Title != title })
	}
	switch len(created) {
	case 1:
		return created[0], nil
	case 0:
		return ConfigSubentry{}, fmt.Errorf("the %s subentry %q was created, but it is not listed", subentryType, title)
	}
	ids := make([]string, len(created))
	for i, s := range created {
		ids[i] = s.SubentryID
	}
	return ConfigSubentry{}, fmt.Errorf("the %s subentry %q was created, but several new subentries match it: %s",
		subentryType, title, strings.Join(ids, ", "))
}

// SupportsReconfigure reports whether the subentries of subentryType of the config entry have a
// reconfigure flow, as HA's `supported_subentry_types` says. A type HA does not list there is
// reported as not reconfigurable. A missing config entry is ErrNotFound.
func (c ConfigSubentries) SupportsReconfigure(ctx context.Context, entryID, subentryType string) (bool, error) {
	var out struct {
		ConfigEntry struct {
			SupportedSubentryTypes map[string]struct {
				SupportsReconfigure bool `json:"supports_reconfigure"`
			} `json:"supported_subentry_types"`
		} `json:"config_entry"`
	}
	err := c.ws.Command(ctx, cmdEntryGetSingle, map[string]any{"entry_id": entryID}, &out)
	if isWSNotFound(err) {
		return false, fmt.Errorf("config entry %s: %w", entryID, ErrNotFound)
	}
	if err != nil {
		return false, err
	}
	return out.ConfigEntry.SupportedSubentryTypes[subentryType].SupportsReconfigure, nil
}

// Reconfigure runs the reconfigure flow of the subentry, which changes its data in place, and
// returns the subentry as it is afterwards. Each form is answered as reconfigureAnswer describes.
// The flow must end with the abort ReasonReconfigureSuccessful. A missing subentry is ErrNotFound.
func (c ConfigSubentries) Reconfigure(ctx context.Context, entryID, subentryType, subentryID string, steps []FlowStep) (ConfigSubentry, error) {
	if _, ok, err := c.Get(ctx, entryID, subentryID); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("subentry %s of config entry %s: %w", subentryID, entryID, ErrNotFound)
		}
		return ConfigSubentry{}, err
	}
	name := fmt.Sprintf("reconfigure flow for %s subentry %s of config entry %s", subentryType, subentryID, entryID)
	init := map[string]any{"handler": []string{entryID, subentryType}, "subentry_id": subentryID}
	res, err := c.driver.run(ctx, name, init, reconfigureAnswer(steps))
	if err != nil {
		return ConfigSubentry{}, err
	}
	if res.Type != "abort" {
		return ConfigSubentry{}, fmt.Errorf("%s ended with %q instead of the abort %q", name, res.Type, ReasonReconfigureSuccessful)
	}
	if res.Reason != ReasonReconfigureSuccessful {
		return ConfigSubentry{}, &FlowAbortedError{Flow: name, Reason: res.Reason}
	}
	sub, ok, err := c.Get(ctx, entryID, subentryID)
	if err == nil && !ok {
		err = fmt.Errorf("subentry %s of config entry %s is gone after %s", subentryID, entryID, name)
	}
	return sub, err
}

// FlowFieldAmbiguousError means a reconfigure form asks for a field that several steps give
// different values, and the step of the form's own step_id does not have it.
type FlowFieldAmbiguousError struct {
	Flow   string
	StepID string
	Field  string
	// Steps are the step_ids that have the field.
	Steps []string
}

func (e *FlowFieldAmbiguousError) Error() string {
	return fmt.Sprintf("%s asked for %q in step %q, which the steps %s set to different values; set it in a step %q",
		e.Flow, e.Field, e.StepID, quoteList(e.Steps), e.StepID)
}

// reconfigureAnswer answers each form of a reconfigure flow from the steps that created the
// subentry. A reconfigure flow often shows the same fields under another step_id than the flow
// that created the subentry (forecast_solar: `user`, then `reconfigure`), or a step without some
// of its fields (an AI integration's `init` without `name`). So each form is answered by its
// data_schema, as FieldAnswer does: a field takes its value from the step of the form's step_id,
// else from whichever step has it. Fields the form does not have are left out, and fields no step
// has too, so HA keeps their value. A field that several other steps give different values is an
// error, as is the same form a second time.
func reconfigureAnswer(steps []FlowStep) FlowAnswer {
	byID := map[string]map[string]any{}
	pool := map[string]any{}
	owners := map[string][]string{}
	ambiguous := map[string]bool{}
	sorted := slices.SortedFunc(slices.Values(steps), func(a, b FlowStep) int { return strings.Compare(a.StepID, b.StepID) })
	for _, s := range sorted {
		in := s.input()
		byID[s.StepID] = in
		for field, v := range in {
			if prev, ok := pool[field]; ok && !reflect.DeepEqual(prev, v) {
				ambiguous[field] = true
			}
			pool[field] = v
			owners[field] = append(owners[field], s.StepID)
		}
	}
	seen := map[string]bool{}
	return func(ctx context.Context, form FlowForm) (map[string]any, error) {
		names := fieldNames(form.Fields)
		slices.Sort(names)
		key := form.StepID + "\x00" + strings.Join(names, "\x00")
		if seen[key] {
			return nil, &FlowStepMissingError{Flow: form.Flow, StepID: form.StepID, Repeated: true}
		}
		seen[key] = true

		own := byID[form.StepID]
		for _, n := range names {
			if _, set := own[n]; ambiguous[n] && !set {
				return nil, &FlowFieldAmbiguousError{Flow: form.Flow, StepID: form.StepID, Field: n, Steps: owners[n]}
			}
		}
		values := maps.Clone(pool)
		maps.Copy(values, own)
		return fillFields(ctx, nil, form, form.Fields, values, nil)
	}
}

// subentryFlows serializes the subentry flows of each config entry within the provider process.
var subentryFlows keyedMutex

// keyedMutex is one mutex per key.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// lock locks the mutex of key and returns its unlock.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*sync.Mutex{}
	}
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// isWSNotFound reports whether err is HA's WebSocket error `not_found`.
func isWSNotFound(err error) bool {
	var wsErr *WSError
	return errors.As(err, &wsErr) && wsErr.Code == "not_found"
}
