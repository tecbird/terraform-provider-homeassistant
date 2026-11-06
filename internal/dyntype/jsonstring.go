package dyntype

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

var (
	_ basetypes.StringTypable                    = JSONStringType{}
	_ basetypes.StringValuableWithSemanticEquals = JSONString{}
)

// JSONStringType is the attribute type of a string that holds JSON, for a dynamic value inside a
// collection, where the framework does not support dynamic attributes. Values are compared by
// semantic equality like dynamic config (ADR-0006): key order and number spelling do not matter,
// so a value HA reports back keeps the user's `jsonencode(...)` text and the plan stays empty.
type JSONStringType struct {
	basetypes.StringType
}

func (t JSONStringType) String() string {
	return "dyntype.JSONStringType"
}

func (t JSONStringType) Equal(o attr.Type) bool {
	_, ok := o.(JSONStringType)
	return ok
}

func (t JSONStringType) ValueType(context.Context) attr.Value {
	return JSONString{}
}

func (t JSONStringType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return JSONString{StringValue: in}, nil
}

func (t JSONStringType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := v.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", v)
	}
	return JSONString{StringValue: sv}, nil
}

// JSONString is a string holding JSON.
type JSONString struct {
	basetypes.StringValue
}

// NewJSONString returns a known JSON string value.
func NewJSONString(s string) JSONString {
	return JSONString{StringValue: basetypes.NewStringValue(s)}
}

// NewJSONStringNull returns a null JSON string value.
func NewJSONStringNull() JSONString {
	return JSONString{StringValue: basetypes.NewStringNull()}
}

func (v JSONString) Type(context.Context) attr.Type {
	return JSONStringType{}
}

func (v JSONString) Equal(o attr.Value) bool {
	other, ok := o.(JSONString)
	return ok && v.StringValue.Equal(other.StringValue)
}

// StringSemanticEquals reports whether both values hold the same JSON after normalisation. The
// framework calls it with the prior value as receiver and the value read back as argument.
func (v JSONString) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics
	other, ok := newValuable.(JSONString)
	if !ok {
		diags.AddError("Semantic equality check error",
			fmt.Sprintf("Expected value type %T but got %T. Please report this to the provider developers.", v, newValuable))
		return false, diags
	}
	if v.IsNull() || v.IsUnknown() || other.IsNull() || other.IsUnknown() {
		return v.Equal(other), nil
	}
	// Text that is not JSON is not equal to anything but itself; validation reports it.
	eq, err := SemanticallyEqualJSON([]byte(v.ValueString()), []byte(other.ValueString()))
	if err != nil {
		return v.Equal(other), nil
	}
	return eq, nil
}
