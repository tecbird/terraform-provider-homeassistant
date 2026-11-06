package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// ConfigEntry is the readable part of a config entry. Its input data cannot be read back
// (ADR-0014).
type ConfigEntry struct {
	EntryID    string  `json:"entry_id"`
	Domain     string  `json:"domain"`
	Title      string  `json:"title"`
	State      string  `json:"state"`
	DisabledBy *string `json:"disabled_by"`
	// SupportsOptions reports whether the integration has an options flow.
	SupportsOptions bool `json:"supports_options"`
}

// ConfigEntries reads config entries over WebSocket (`config_entries/get`) and deletes them over
// REST (`/api/config/config_entries/entry/<id>`, ADR-0004).
type ConfigEntries struct {
	ws   WSCommander
	rest *RESTClient
}

// NewConfigEntries returns the config-entry API on ws and rest.
func NewConfigEntries(ws WSCommander, rest *RESTClient) ConfigEntries {
	return ConfigEntries{ws: ws, rest: rest}
}

// List returns the config entries of domain, or of every domain if domain is empty.
func (c ConfigEntries) List(ctx context.Context, domain string) ([]ConfigEntry, error) {
	params := map[string]any{}
	if domain != "" {
		params["domain"] = domain
	}
	var entries []ConfigEntry
	err := c.ws.Command(ctx, "config_entries/get", params, &entries)
	return entries, err
}

// Get returns the config entry with the given ID, looking only at domain unless it is empty. ok is
// false if there is none.
func (c ConfigEntries) Get(ctx context.Context, domain, entryID string) (entry ConfigEntry, ok bool, err error) {
	entries, err := c.List(ctx, domain)
	if err != nil {
		return entry, false, err
	}
	for _, e := range entries {
		if e.EntryID == entryID {
			return e, true, nil
		}
	}
	return entry, false, nil
}

// Delete deletes the config entry with the given ID. requireRestart reports that HA needs a
// restart to finish removing the integration. An entry that is already gone is not an error.
func (c ConfigEntries) Delete(ctx context.Context, entryID string) (requireRestart bool, err error) {
	var out struct {
		RequireRestart bool `json:"require_restart"`
	}
	err = c.rest.Do(ctx, http.MethodDelete, "config/config_entries/entry/"+url.PathEscape(entryID), nil, &out)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return out.RequireRestart, err
}
