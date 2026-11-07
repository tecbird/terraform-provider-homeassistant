package resources

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

// helperSpec describes one input helper domain (ADR-0013). Every helper shares the generic
// implementation in helperResource.
type helperSpec struct {
	// domain is the helper's domain, e.g. "input_boolean".
	domain string
	// description is the resource description.
	description string
	// fields are the helper's attributes besides `id`, `name`, `icon`, and `entity_id`. Each is
	// stored in HA under the attribute's name.
	fields map[string]schema.Attribute
	// validate, if set, checks the config at plan time. It gets the config's attributes; values
	// that are not known yet are unknown, and it skips them.
	validate func(attrs map[string]attr.Value) diag.Diagnostics
}

var (
	_ resource.ResourceWithConfigure      = (*helperResource)(nil)
	_ resource.ResourceWithImportState    = (*helperResource)(nil)
	_ resource.ResourceWithValidateConfig = (*helperResource)(nil)
)

// newHelper returns the resource for a helper domain.
func newHelper(spec helperSpec) resource.Resource {
	return &helperResource{spec: spec}
}

// helperResource manages helpers of one domain. Its model is the whole resource object, so the
// same code serves every helper's schema: attributes other than `id` and `entity_id` map 1:1 to
// the helper's fields in HA.
type helperResource struct {
	spec    helperSpec
	helpers client.Registry[client.Helper]
}

func (r *helperResource) typeName() string {
	return "homeassistant_" + r.spec.domain
}

func (r *helperResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.spec.domain
}

func (r *helperResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Description: "Helper ID, a slug such as `guest_mode`. Home Assistant derives it from `name` " +
				"when the helper is created, and keeps it when the helper is renamed. Set it to choose the ID " +
				"yourself. Changing it replaces the helper. Creation fails if the ID is taken. Import with " +
				"this ID.",
			Optional: true,
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				stringplanmodifier.RequiresReplaceIfConfigured(),
			},
			Validators: []validator.String{slugValidator{}},
		},
		"name": schema.StringAttribute{
			Description: "Display name. Changing it renames the helper in place.",
			Required:    true,
		},
		"icon": schema.StringAttribute{
			Description: "Icon, e.g. `mdi:toggle-switch`.",
			Optional:    true,
		},
		"entity_id": schema.StringAttribute{
			Description: fmt.Sprintf("Entity ID of the helper, `%s.<id>`.", r.spec.domain),
			Computed:    true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
	}
	for name, a := range r.spec.fields {
		attrs[name] = a
	}
	resp.Schema = schema.Schema{Description: r.spec.description, Attributes: attrs}
}

func (r *helperResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if r.spec.validate == nil {
		return
	}
	var cfg types.Object
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.spec.validate(cfg.Attributes())...)
}

func (r *helperResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.helpers = client.Helpers(c.WS, r.spec.domain)
}

// helperFields returns the helper's fields from the resource object m, including `name`. HA
// replaces the whole helper on update, so unset attributes are left out, which clears them.
func helperFields(m types.Object) (map[string]any, error) {
	fields := map[string]any{}
	for name, v := range m.Attributes() {
		if name == "id" || name == "entity_id" || v.IsNull() || v.IsUnknown() {
			continue
		}
		f, err := haValue(v)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", name, err)
		}
		fields[name] = f
	}
	return fields, nil
}

// haValue converts a known, non-null attribute value into the JSON-like value HA stores. Null
// attributes of an object are left out.
func haValue(v attr.Value) (any, error) {
	switch v := v.(type) {
	case dyntype.JSONString:
		// A JSON string is passed to HA as the JSON it holds.
		return json.RawMessage(v.ValueString()), nil
	case types.String:
		return v.ValueString(), nil
	case types.Bool:
		return v.ValueBool(), nil
	case types.Float64:
		return v.ValueFloat64(), nil
	case types.Int64:
		return v.ValueInt64(), nil
	case types.List:
		items := make([]any, 0, len(v.Elements()))
		for _, e := range v.Elements() {
			if e.IsNull() || e.IsUnknown() {
				return nil, fmt.Errorf("has an element %v that is not known. This is a bug in the provider", e)
			}
			item, err := haValue(e)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		return items, nil
	case types.Object:
		obj := map[string]any{}
		for name, e := range v.Attributes() {
			if e.IsNull() {
				continue
			}
			if e.IsUnknown() {
				return nil, fmt.Errorf("has the unknown attribute %q. This is a bug in the provider", name)
			}
			f, err := haValue(e)
			if err != nil {
				return nil, fmt.Errorf("%q: %w", name, err)
			}
			obj[name] = f
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("has the unsupported type %T. This is a bug in the provider", v)
	}
}

// helperObject converts a helper as HA stores it into the resource object of type t. Fields HA
// does not report are null.
func (r *helperResource) helperObject(t types.ObjectType, h client.Helper) (types.Object, diag.Diagnostics) {
	vals := map[string]attr.Value{}
	for name, typ := range t.AttrTypes {
		raw, set := h[name]
		switch name {
		case "id":
			raw, set = h.HelperID(), true
		case "entity_id":
			raw, set = r.spec.domain+"."+h.HelperID(), true
		}
		if !set {
			raw = nil
		}
		v, err := attrValue(typ, raw)
		if err != nil {
			var diags diag.Diagnostics
			diags.AddError("Reading helper", fmt.Sprintf("Attribute %q: %s. This is a bug in the provider.", name, err))
			return types.ObjectNull(t.AttrTypes), diags
		}
		vals[name] = v
	}
	return types.ObjectValue(t.AttrTypes, vals)
}

// attrValue converts a JSON-decoded value from HA into an attribute value of type typ. A missing
// value, or one of the wrong JSON type, is null.
func attrValue(typ attr.Type, raw any) (attr.Value, error) {
	if _, ok := typ.(dyntype.JSONStringType); ok {
		if raw == nil {
			return dyntype.NewJSONStringNull(), nil
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return dyntype.NewJSONString(string(b)), nil
	}
	switch {
	case typ.Equal(types.StringType):
		if s, ok := raw.(string); ok {
			return types.StringValue(s), nil
		}
		return types.StringNull(), nil
	case typ.Equal(types.BoolType):
		if b, ok := raw.(bool); ok {
			return types.BoolValue(b), nil
		}
		return types.BoolNull(), nil
	case typ.Equal(types.Float64Type):
		if f, ok := raw.(float64); ok {
			return types.Float64Value(f), nil
		}
		return types.Float64Null(), nil
	case typ.Equal(types.Int64Type):
		// JSON numbers decode to float64.
		if f, ok := raw.(float64); ok {
			return types.Int64Value(int64(f)), nil
		}
		return types.Int64Null(), nil
	}
	switch typ := typ.(type) {
	case types.ListType:
		items, ok := raw.([]any)
		if !ok {
			return types.ListNull(typ.ElemType), nil
		}
		elems := make([]attr.Value, 0, len(items))
		for _, item := range items {
			e, err := attrValue(typ.ElemType, item)
			if err != nil {
				return nil, err
			}
			elems = append(elems, e)
		}
		return types.ListValueMust(typ.ElemType, elems), nil
	case types.ObjectType:
		obj, ok := raw.(map[string]any)
		if !ok {
			return types.ObjectNull(typ.AttrTypes), nil
		}
		vals := make(map[string]attr.Value, len(typ.AttrTypes))
		for name, at := range typ.AttrTypes {
			v, err := attrValue(at, obj[name])
			if err != nil {
				return nil, err
			}
			vals[name] = v
		}
		return types.ObjectValueMust(typ.AttrTypes, vals), nil
	}
	return nil, fmt.Errorf("has the unsupported type %s", typ)
}

func (r *helperResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fields, err := helperFields(plan)
	if err != nil {
		resp.Diagnostics.AddError("Creating helper", err.Error())
		return
	}
	var planID types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("id"), &planID)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// withName returns the fields with name replaced.
	withName := func(name string) map[string]any {
		f := make(map[string]any, len(fields))
		for k, v := range fields {
			f[k] = v
		}
		f["name"] = name
		return f
	}
	g := generatedID{
		kind:     "helper",
		typeName: r.typeName(),
		create: func(ctx context.Context, name string) (string, error) {
			h, err := r.helpers.Create(ctx, withName(name))
			return h.HelperID(), err
		},
		rename: func(ctx context.Context, id, name string) error {
			_, err := r.helpers.Update(ctx, id, withName(name))
			return err
		},
		delete: r.helpers.Delete,
	}
	id, err := g.Create(ctx, fields["name"].(string), planID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Creating helper", client.ErrorDetail(err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("entity_id"), r.spec.domain+"."+id)...)
}

func (r *helperResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	h, ok, err := r.helpers.Get(ctx, id.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading helper", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}

	obj, diags := r.helperObject(state.Type(ctx).(types.ObjectType), h)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, obj)...)
}

func (r *helperResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan types.Object
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fields, err := helperFields(plan)
	if err != nil {
		resp.Diagnostics.AddError("Updating helper", err.Error())
		return
	}

	if _, err := r.helpers.Update(ctx, id.ValueString(), fields); err != nil {
		resp.Diagnostics.AddError("Updating helper", client.ErrorDetail(err))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *helperResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.helpers.Delete(ctx, id.ValueString()); err != nil {
		resp.Diagnostics.AddError("Deleting helper", client.ErrorDetail(err))
	}
}

func (r *helperResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// configValue returns the config attribute `name` as T. set is false if it is null; known is
// false if it is unknown. Plan-time checks skip unknown values.
func configValue[T attr.Value](attrs map[string]attr.Value, name string) (v T, set, known bool) {
	a, ok := attrs[name]
	if !ok {
		return v, false, true
	}
	v, _ = a.(T)
	return v, !a.IsNull(), !a.IsUnknown()
}
