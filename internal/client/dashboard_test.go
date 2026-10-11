package client

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// fakeCommander records commands and answers each with the next scripted reply.
type fakeCommander struct {
	calls   []fakeCall
	replies []fakeReply
}

type fakeCall struct {
	typ    string
	params map[string]any
}

type fakeReply struct {
	result string // JSON
	err    error
}

func (f *fakeCommander) Command(_ context.Context, typ string, params map[string]any, result any) error {
	f.calls = append(f.calls, fakeCall{typ, params})
	r := f.replies[0]
	f.replies = f.replies[1:]
	if r.err != nil {
		return r.err
	}
	if result != nil && r.result != "" {
		return json.Unmarshal([]byte(r.result), result)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

func TestDashboardsGet(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{{result: `[
		{"id":"map","url_path":"map","mode":"storage","title":"Map","icon":"mdi:map","show_in_sidebar":true,"require_admin":false},
		{"id":"my_home","url_path":"my-home","mode":"storage","title":"My Home","show_in_sidebar":false,"require_admin":true}
	]`}}}
	d, ok, err := NewDashboards(f).Get(t.Context(), "my-home")
	if err != nil || !ok {
		t.Fatalf("Get: ok = %v, err = %v", ok, err)
	}
	want := Dashboard{ID: "my_home", URLPath: "my-home", Mode: "storage", Title: "My Home", RequireAdmin: true}
	if d != want {
		t.Errorf("Get = %+v, want %+v", d, want)
	}
	if f.calls[0].typ != "lovelace/dashboards/list" {
		t.Errorf("command = %q", f.calls[0].typ)
	}

	f.replies = []fakeReply{{result: `[]`}}
	if _, ok, err := NewDashboards(f).Get(t.Context(), "my-home"); err != nil || ok {
		t.Errorf("Get on empty list: ok = %v, err = %v; want not found", ok, err)
	}
}

func TestDashboardsCreateAndUpdate_IconHandling(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{{result: `{"id":"my_home"}`}, {}, {}}}
	d := NewDashboards(f)
	meta := DashboardMeta{Title: "My Home", ShowInSidebar: true}

	if _, err := d.Create(t.Context(), "my-home", meta); err != nil {
		t.Fatal(err)
	}
	// Create rejects a null icon, so it is left out.
	wantCreate := map[string]any{"url_path": "my-home", "title": "My Home", "show_in_sidebar": true, "require_admin": false}
	if !reflect.DeepEqual(f.calls[0].params, wantCreate) {
		t.Errorf("create params = %v, want %v", f.calls[0].params, wantCreate)
	}

	// Update sends a null icon, which clears it.
	if err := d.Update(t.Context(), "my_home", meta); err != nil {
		t.Fatal(err)
	}
	if icon, ok := f.calls[1].params["icon"]; !ok || icon.(*string) != nil {
		t.Errorf("update icon = %v (present %v), want explicit null", icon, ok)
	}
	if f.calls[1].params["dashboard_id"] != "my_home" {
		t.Errorf("update dashboard_id = %v", f.calls[1].params["dashboard_id"])
	}

	meta.Icon = ptr("mdi:home")
	if err := d.Update(t.Context(), "my_home", meta); err != nil {
		t.Fatal(err)
	}
	if icon := f.calls[2].params["icon"].(*string); icon == nil || *icon != "mdi:home" {
		t.Errorf("update icon = %v, want mdi:home", icon)
	}
}

func TestDashboardsConfig(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{
		{result: `{"views":[{"title":"Home"}]}`},
		{err: &WSError{Code: "config_not_found", Message: "No config found."}},
		{err: &WSError{Code: "unknown_error", Message: "boom"}},
	}}
	d := NewDashboards(f)

	raw, err := d.Config(t.Context(), "my-home")
	if err != nil || string(raw) != `{"views":[{"title":"Home"}]}` {
		t.Errorf("Config = %s, %v", raw, err)
	}
	if want := map[string]any{"url_path": "my-home", "force": true}; !reflect.DeepEqual(f.calls[0].params, want) {
		t.Errorf("config params = %v, want %v", f.calls[0].params, want)
	}
	if _, err := d.Config(t.Context(), "my-home"); !errors.Is(err, ErrNoDashboardConfig) {
		t.Errorf("Config without saved config: err = %v, want ErrNoDashboardConfig", err)
	}
	if _, err := d.Config(t.Context(), "my-home"); err == nil || errors.Is(err, ErrNoDashboardConfig) {
		t.Errorf("Config on other error: err = %v, want it passed through", err)
	}
}

// The default dashboard has no url_path: the config commands leave the field out.
func TestDashboardsDefaultConfigCommandsOmitURLPath(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{
		{result: `{"views":[]}`},
		{err: &WSError{Code: "config_not_found", Message: "No config found."}},
		{},
		{},
	}}
	d := NewDashboards(f)
	cfg := json.RawMessage(`{"views":[]}`)

	if raw, err := d.Config(t.Context(), DefaultDashboardURLPath); err != nil || string(raw) != `{"views":[]}` {
		t.Errorf("Config = %s, %v", raw, err)
	}
	if _, err := d.Config(t.Context(), DefaultDashboardURLPath); !errors.Is(err, ErrNoDashboardConfig) {
		t.Errorf("Config of an auto-generated default: err = %v, want ErrNoDashboardConfig", err)
	}
	if err := d.SaveConfig(t.Context(), DefaultDashboardURLPath, cfg); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteConfig(t.Context(), DefaultDashboardURLPath); err != nil {
		t.Fatal(err)
	}

	want := []fakeCall{
		{"lovelace/config", map[string]any{"force": true}},
		{"lovelace/config", map[string]any{"force": true}},
		{"lovelace/config/save", map[string]any{"config": cfg}},
		{"lovelace/config/delete", map[string]any{}},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

func TestDashboardsDeleteConfigNamesTheDashboard(t *testing.T) {
	f := &fakeCommander{replies: []fakeReply{{}}}
	if err := NewDashboards(f).DeleteConfig(t.Context(), "my-home"); err != nil {
		t.Fatal(err)
	}
	if want := (fakeCall{"lovelace/config/delete", map[string]any{"url_path": "my-home"}}); !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("call = %v, want %v", f.calls[0], want)
	}
}
