package client

import (
	"encoding/json"
	"testing"
)

func TestRegistryStringDecode(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want string
	}{
		{"string", `"1.2"`, "1.2"},
		{"integer", `62092`, "62092"},
		{"float", `1.5`, "1.5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var s RegistryString
			if err := json.Unmarshal([]byte(tt.in), &s); err != nil {
				t.Fatal(err)
			}
			if string(s) != tt.want {
				t.Errorf("%s = %q, want %q", tt.in, s, tt.want)
			}
		})
	}
}

func TestRegistryStringDecodeRejectsOtherValues(t *testing.T) {
	for _, in := range []string{`null`, `true`} {
		var s RegistryString
		if err := json.Unmarshal([]byte(in), &s); err == nil {
			t.Errorf("%s decoded to %q, want an error", in, s)
		}
	}
}

func TestStringRows(t *testing.T) {
	rows := [][]RegistryString{{"broadlink", "1234"}, {}}
	if got := StringRows(rows); len(got) != 2 || got[0][0] != "broadlink" || got[0][1] != "1234" || len(got[1]) != 0 {
		t.Errorf("StringRows(%v) = %v", rows, got)
	}
}
