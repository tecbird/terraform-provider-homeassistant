package resources

import (
	"fmt"
	"strings"
	"testing"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

func TestParseSubentryID(t *testing.T) {
	entryID, subentryID, err := parseSubentryID("01JENTRY/01JSUB")
	if err != nil || entryID != "01JENTRY" || subentryID != "01JSUB" {
		t.Errorf("parseSubentryID = %q, %q, %v", entryID, subentryID, err)
	}
	for _, id := range []string{"", "01JENTRY", "01JENTRY/", "/01JSUB", "a/b/c"} {
		if _, _, err := parseSubentryID(id); err == nil || !strings.Contains(err.Error(), "<entry_id>/<subentry_id>") {
			t.Errorf("parseSubentryID(%q) = %v, want an error", id, err)
		}
	}
}

func TestSubentryErrorDetail(t *testing.T) {
	cases := map[string]struct {
		err  error
		hint string
	}{
		"already configured": {
			err:  &client.FlowAbortedError{Flow: "subentry flow", Reason: "already_configured"},
			hint: "tofu import <resource address> <entry_id>/<subentry_id>",
		},
		// The unknown type wraps the 404, but gets its own hint, not the one for a missing entry.
		"unknown type": {
			err:  &client.SubentryTypeUnknownError{EntryID: "E1", SubentryType: "x", Err: client.ErrNotFound},
			hint: "Check `subentry_type`",
		},
		"entry gone": {
			err:  fmt.Errorf("config entry E9: %w", client.ErrNotFound),
			hint: "Check `entry_id`",
		},
		"form error": {
			err:  &client.FlowFormError{Flow: "subentry flow", StepID: "user", Errors: map[string]string{"base": "cannot_connect"}},
			hint: "could not connect",
		},
		"unreachable": {
			err:  client.ErrUnreachable,
			hint: "-refresh=false",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := subentryErrorDetail(tc.err)
			if !strings.HasPrefix(got, tc.err.Error()) || !strings.Contains(got, tc.hint) {
				t.Errorf("subentryErrorDetail = %q, want the error and %q", got, tc.hint)
			}
		})
	}
}
