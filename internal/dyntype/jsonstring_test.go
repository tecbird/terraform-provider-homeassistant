package dyntype

import (
	"context"
	"testing"
)

func TestJSONStringSemanticEquals(t *testing.T) {
	for _, tc := range []struct {
		name, a, b string
		want       bool
	}{
		{"key order", `{"a":1,"b":"x"}`, `{"b":"x","a":1}`, true},
		{"number spelling", `{"a":1.0}`, `{"a":1}`, true},
		{"whitespace", `{ "a" : true }`, `{"a":true}`, true},
		{"different value", `{"a":1}`, `{"a":2}`, false},
		{"different type", `{"a":1}`, `{"a":"1"}`, false},
		{"not JSON, same text", `x`, `x`, true},
		{"not JSON, other text", `x`, `y`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, diags := NewJSONString(tc.a).StringSemanticEquals(context.Background(), NewJSONString(tc.b))
			if diags.HasError() || got != tc.want {
				t.Errorf("equal = %v (diags %v), want %v", got, diags, tc.want)
			}
		})
	}
	if got, _ := NewJSONStringNull().StringSemanticEquals(context.Background(), NewJSONString(`{}`)); got {
		t.Error("null equals a value")
	}
}
