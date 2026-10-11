package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSubentryHA serves the subentries of config entry E1: `config_entries/subentries/list` and
// `/delete` over WebSocket, and subentry flows over REST. A flow shows the form `user`, and the
// data posted to it creates a subentry titled by its field `name`, as kitchen_sink's sensors are.
type fakeSubentryHA struct {
	t *testing.T
	// types are the subentry types E1 offers.
	types []string
	// abort, if set, is the reason the flow aborts with instead of creating the subentry.
	abort string
	// bothStarted, if set, is closed once two flows have started; each create_entry waits for
	// that, or for a timeout.
	bothStarted chan struct{}

	mu         sync.Mutex
	subentries []ConfigSubentry
	flows      int
	inits      []map[string]any
	submitted  []map[string]any
	deleted    []string
}

func (f *fakeSubentryHA) client() ConfigSubentries {
	return NewConfigSubentries(f, serve(f.t, f.handle))
}

func (f *fakeSubentryHA) Command(_ context.Context, typ string, params map[string]any, result any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if params["entry_id"] != "E1" {
		return &WSError{Code: "not_found", Message: "Config entry not found"}
	}
	switch typ {
	case "config_entries/subentries/list":
		out := result.(*[]ConfigSubentry)
		*out = append([]ConfigSubentry(nil), f.subentries...)
		return nil
	case "config_entries/subentries/delete":
		for i, s := range f.subentries {
			if s.SubentryID == params["subentry_id"] {
				f.subentries = append(f.subentries[:i], f.subentries[i+1:]...)
				return nil
			}
		}
		return &WSError{Code: "not_found", Message: "Config subentry not found"}
	}
	return &WSError{Code: "unknown_command", Message: typ}
}

func (f *fakeSubentryHA) handle(w http.ResponseWriter, r *http.Request) {
	const base = "/api/config/config_entries/subentries/flow"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base:
		body := decodeBody(f.t, r)
		f.mu.Lock()
		f.inits = append(f.inits, body)
		handler, _ := body["handler"].([]any)
		known := len(handler) == 2 && handler[0] == "E1" && strings.Contains(" "+strings.Join(f.types, " ")+" ", fmt.Sprintf(" %v ", handler[1]))
		f.flows++
		id := fmt.Sprintf("f%d", f.flows)
		if f.bothStarted != nil && f.flows == 2 {
			close(f.bothStarted)
		}
		f.mu.Unlock()
		if !known {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Invalid handler specified"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"type":"form","flow_id":%q,"step_id":"user","errors":{}}`, id)

	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, base+"/"):
		id := strings.TrimPrefix(r.URL.Path, base+"/")
		data := decodeBody(f.t, r)
		if f.bothStarted != nil {
			select {
			case <-f.bothStarted:
			case <-time.After(200 * time.Millisecond):
			}
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.submitted = append(f.submitted, data)
		if f.abort != "" {
			_, _ = fmt.Fprintf(w, `{"type":"abort","flow_id":%q,"reason":%q}`, id, f.abort)
			return
		}
		title, _ := data["name"].(string)
		f.subentries = append(f.subentries, ConfigSubentry{
			SubentryID: fmt.Sprintf("S%d", len(f.subentries)+1), SubentryType: "entity", Title: title,
		})
		// Like HA's, the reply does not name the new subentry.
		_, _ = fmt.Fprintf(w, `{"type":"create_entry","flow_id":%q,"handler":["E1","entity"],"title":%q,"unique_id":null}`, id, title)

	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, base+"/"):
		f.mu.Lock()
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, base+"/"))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"message":"Flow aborted"}`))

	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func TestConfigSubentriesCreate(t *testing.T) {
	f := &fakeSubentryHA{t: t, types: []string{"entity"}, subentries: []ConfigSubentry{
		{SubentryID: "S0", SubentryType: "entity", Title: "Existing"},
	}}

	got, err := f.client().Create(t.Context(), "E1", "entity", []FlowStep{
		{StepID: "user", Data: map[string]any{"name": "Sensor", "state": 3}, SensitiveData: map[string]any{"state": 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := (ConfigSubentry{SubentryID: "S2", SubentryType: "entity", Title: "Sensor"}); !reflect.DeepEqual(got, want) {
		t.Errorf("Create = %+v, want %+v", got, want)
	}
	if want := []map[string]any{{"handler": []any{"E1", "entity"}}}; !reflect.DeepEqual(f.inits, want) {
		t.Errorf("init = %v", f.inits)
	}
	if want := []map[string]any{{"name": "Sensor", "state": float64(5)}}; !reflect.DeepEqual(f.submitted, want) {
		t.Errorf("submitted = %v", f.submitted)
	}
}

func TestConfigSubentriesCreateErrors(t *testing.T) {
	steps := []FlowStep{{StepID: "user", Data: map[string]any{"name": "Sensor"}}}

	t.Run("entry gone", func(t *testing.T) {
		f := &fakeSubentryHA{t: t, types: []string{"entity"}}
		_, err := f.client().Create(t.Context(), "E9", "entity", steps)
		if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "config entry E9") {
			t.Fatalf("err = %v", err)
		}
		if len(f.inits) != 0 {
			t.Errorf("a flow was started: %v", f.inits)
		}
	})

	t.Run("unknown type", func(t *testing.T) {
		f := &fakeSubentryHA{t: t, types: []string{"entity"}}
		_, err := f.client().Create(t.Context(), "E1", "plane", steps)
		var unknown *SubentryTypeUnknownError
		if !errors.As(err, &unknown) || unknown.SubentryType != "plane" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), `config entry E1 has no subentry type "plane"`) {
			t.Errorf("message = %q", err)
		}
	})

	t.Run("abort", func(t *testing.T) {
		f := &fakeSubentryHA{t: t, types: []string{"entity"}, abort: "api_key_required"}
		_, err := f.client().Create(t.Context(), "E1", "entity", steps)
		var aborted *FlowAbortedError
		if !errors.As(err, &aborted) || aborted.Reason != "api_key_required" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "subentry flow for entity of config entry E1 aborted: api_key_required") {
			t.Errorf("message = %q", err)
		}
	})

	t.Run("missing step", func(t *testing.T) {
		f := &fakeSubentryHA{t: t, types: []string{"entity"}}
		_, err := f.client().Create(t.Context(), "E1", "entity", []FlowStep{{StepID: "add_sensor"}})
		var missing *FlowStepMissingError
		if !errors.As(err, &missing) || missing.StepID != "user" {
			t.Fatalf("err = %v", err)
		}
		if !reflect.DeepEqual(f.deleted, []string{"f1"}) {
			t.Errorf("deleted = %v", f.deleted)
		}
	})
}

// Two subentries with the same title are created at once on the same entry: the flows run one at a
// time, so each finds its own subentry. Were they to overlap, the fake would let both start before
// either creates its subentry, and the later one could not tell the two apart.
func TestConfigSubentriesCreateConcurrently(t *testing.T) {
	f := &fakeSubentryHA{t: t, types: []string{"entity"}, bothStarted: make(chan struct{})}
	c := f.client()

	var wg sync.WaitGroup
	got := make([]ConfigSubentry, 2)
	errs := make([]error, 2)
	for i := range got {
		wg.Go(func() {
			got[i], errs[i] = c.Create(t.Context(), "E1", "entity", []FlowStep{
				{StepID: "user", Data: map[string]any{"name": "Same"}},
			})
		})
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got[0].SubentryID == got[1].SubentryID {
		t.Errorf("both creates returned %s", got[0].SubentryID)
	}
}

func TestNewSubentry(t *testing.T) {
	before := []ConfigSubentry{{SubentryID: "S1", SubentryType: "plane", Title: "A"}}
	cases := map[string]struct {
		after   []ConfigSubentry
		want    string
		wantErr string
	}{
		"one new": {
			after: append(before, ConfigSubentry{SubentryID: "S2", SubentryType: "plane", Title: "B"}),
			want:  "S2",
		},
		"other type ignored": {
			after: append(before,
				ConfigSubentry{SubentryID: "S2", SubentryType: "plane", Title: "B"},
				ConfigSubentry{SubentryID: "S3", SubentryType: "other", Title: "B"}),
			want: "S2",
		},
		"picked by title": {
			after: append(before,
				ConfigSubentry{SubentryID: "S2", SubentryType: "plane", Title: "C"},
				ConfigSubentry{SubentryID: "S3", SubentryType: "plane", Title: "B"}),
			want: "S3",
		},
		"none": {after: before, wantErr: "was created, but it is not listed"},
		"ambiguous": {
			after: append(before,
				ConfigSubentry{SubentryID: "S2", SubentryType: "plane", Title: "B"},
				ConfigSubentry{SubentryID: "S3", SubentryType: "plane", Title: "B"}),
			wantErr: "several new subentries match it: S2, S3",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := newSubentry(before, tc.after, "plane", "B")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.SubentryID != tc.want {
				t.Errorf("newSubentry = %+v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestConfigSubentriesGet(t *testing.T) {
	uid := "u1"
	f := &fakeSubentryHA{t: t, subentries: []ConfigSubentry{
		{SubentryID: "S1", SubentryType: "plane", Title: "South", UniqueID: &uid},
	}}
	c := f.client()

	got, ok, err := c.Get(t.Context(), "E1", "S1")
	if err != nil || !ok || !reflect.DeepEqual(got, f.subentries[0]) {
		t.Errorf("Get(S1) = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := c.Get(t.Context(), "E1", "S9"); ok || err != nil {
		t.Errorf("Get(S9) = %v, %v; want not found", ok, err)
	}
	// The config entry is gone, and with it the subentry.
	if _, ok, err := c.Get(t.Context(), "E9", "S1"); ok || err != nil {
		t.Errorf("Get(E9) = %v, %v; want not found", ok, err)
	}
}

func TestConfigSubentriesGetOtherError(t *testing.T) {
	c := NewConfigSubentries(fakeWS{"config_entries/subentries/list": func(map[string]any) (any, error) {
		return nil, &WSError{Code: "unauthorized", Message: "Unauthorized"}
	}}, nil)
	if _, _, err := c.Get(t.Context(), "E1", "S1"); err == nil {
		t.Error("Get = nil error")
	}
}

func TestConfigSubentriesDelete(t *testing.T) {
	f := &fakeSubentryHA{t: t, subentries: []ConfigSubentry{{SubentryID: "S1"}, {SubentryID: "S2"}}}
	c := f.client()

	if err := c.Delete(t.Context(), "E1", "S1"); err != nil {
		t.Fatal(err)
	}
	if want := []ConfigSubentry{{SubentryID: "S2"}}; !reflect.DeepEqual(f.subentries, want) {
		t.Errorf("subentries = %+v", f.subentries)
	}
	// A subentry or config entry that is already gone is not an error.
	if err := c.Delete(t.Context(), "E1", "S1"); err != nil {
		t.Errorf("Delete(S1) again = %v", err)
	}
	if err := c.Delete(t.Context(), "E9", "S2"); err != nil {
		t.Errorf("Delete(E9) = %v", err)
	}

	failing := NewConfigSubentries(fakeWS{"config_entries/subentries/delete": func(map[string]any) (any, error) {
		return nil, &WSError{Code: "unknown_error", Message: "boom"}
	}}, nil)
	if err := failing.Delete(t.Context(), "E1", "S2"); err == nil {
		t.Error("Delete = nil error")
	}
}
