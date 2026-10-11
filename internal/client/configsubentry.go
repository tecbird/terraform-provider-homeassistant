package client

import (
	"context"
	"errors"
	"fmt"
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
	subentryFlowPath    = "config/config_entries/subentries/flow"
)

// ConfigSubentries reads and deletes the subentries of config entries over WebSocket
// (`config_entries/subentries/list|delete`) and creates them through subentry flows over REST
// (`/api/config/config_entries/subentries/flow`, ADR-0004).
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
