package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

var (
	_ resource.ResourceWithConfigure   = (*integrationResource)(nil)
	_ resource.ResourceWithImportState = (*integrationResource)(nil)
)

// NewIntegration returns the generic `homeassistant_integration` resource (ADR-0014).
func NewIntegration() resource.Resource {
	return &integrationResource{}
}

type integrationResource struct {
	flows   client.ConfigFlows
	entries client.ConfigEntries
}

type integrationModel struct {
	entryModel
	Domain         types.String  `tfsdk:"domain"`
	Steps          dyntype.Value `tfsdk:"steps"`
	SensitiveSteps dyntype.Value `tfsdk:"sensitive_steps"`
}

var stepsPath = path.Root("steps")

func (r *integrationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration"
}

func (r *integrationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceUnlessImported := dynamicplanmodifier.RequiresReplaceIf(stepsChanged,
		"Changing the steps replaces the config entry, except right after an import.",
		"Changing the steps replaces the config entry, except right after an import.")
	attrs := entryAttributes()
	maps.Copy(attrs, map[string]schema.Attribute{
		"domain": schema.StringAttribute{
			Description:   "Integration domain, e.g. `shelly`. Changing it replaces the entry.",
			Required:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"steps": schema.DynamicAttribute{
			Description: "The answers to the config flow's forms, as an object from `step_id` to " +
				"the form's fields, e.g. `{ user = { host = \"192.168.1.50\" } }`. Each form Home " +
				"Assistant shows is answered by the step with its `step_id`; a form without fields " +
				"is answered by `{}`. A flow that shows the same `step_id` more than once is answered by " +
				"a list of objects for that `step_id`, e.g. `{ user = [{ backend_type = \"ollama\" }, " +
				"{ host = \"localhost\" }] }`: the entries answer its forms in order. An object is " +
				"a single answer, so a second showing of that form is an error. If Home Assistant " +
				"asks for a step that is not given, shows a form more often than it has answers, or " +
				"rejects the data, the flow is aborted and nothing is created. Changing the steps " +
				"replaces the entry, except right after an import, when they are only recorded.",
			CustomType:    dyntype.Type{},
			Required:      true,
			Validators:    []validator.Dynamic{stepsValidator{}},
			PlanModifiers: []planmodifier.Dynamic{replaceUnlessImported},
		},
		"sensitive_steps": schema.DynamicAttribute{
			Description: "More fields, in the same shape as `steps`, that are hidden in plan " +
				"output, e.g. `{ user = { password = var.password } }`. They are merged into the " +
				"step of the same `step_id`; on a field in both, this one wins. A `step_id` given as a " +
				"list in `steps` needs a list of the same length here, with `{}` for a form without " +
				"secret fields. They are still stored in the state.",
			CustomType:    dyntype.Type{},
			Optional:      true,
			Sensitive:     true,
			Validators:    []validator.Dynamic{stepsValidator{sensitive: true}},
			PlanModifiers: []planmodifier.Dynamic{replaceUnlessImported},
		},
	})
	resp.Schema = schema.Schema{
		Description: "A config entry of an integration, created by answering its config flow with " +
			"the given steps. Home Assistant cannot return the data an entry was created with, so " +
			"the steps are never compared with Home Assistant: changing them replaces the entry, " +
			"and only an entry that is gone is detected as drift. Flows that need a menu, a " +
			"browser login, or a button press on a device are not supported.",
		Attributes: attrs,
	}
}

// stepsChanged requires replacement unless the prior `steps` are null, which they are only after
// an import: the steps cannot be read back, so the first ones written are recorded, not applied.
func stepsChanged(ctx context.Context, req planmodifier.DynamicRequest, resp *dynamicplanmodifier.RequiresReplaceIfFuncResponse) {
	var prior dyntype.Value
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, stepsPath, &prior)...)
	resp.RequiresReplace = !prior.IsNull() && !prior.IsUnderlyingValueNull()
}

func (r *integrationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.flows = client.NewConfigFlows(c.REST)
	r.entries = client.NewConfigEntries(c.WS, c.REST)
}

// flowSteps merges `steps` and `sensitive_steps` into the steps of the config-flow driver. A step
// given as a list yields one driver step per form, in order, all with the step's step_id.
func flowSteps(steps, sensitive dyntype.Value) ([]client.FlowStep, diag.Diagnostics) {
	var diags diag.Diagnostics
	data, err := decodeSteps(steps)
	if err != nil {
		diags.AddAttributeError(stepsPath, "Invalid steps", err.Error())
	}
	secret, err := decodeSteps(sensitive)
	if err != nil {
		// Do not echo the value: it is sensitive.
		diags.AddAttributeError(path.Root("sensitive_steps"), "Invalid steps",
			"sensitive_steps must be an object from step_id to an object of fields or a list of such objects.")
	}
	ids := map[string]bool{}
	for id := range data {
		ids[id] = true
	}
	for id := range secret {
		ids[id] = true
	}
	var out []client.FlowStep
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		d, hasData := data[id]
		sec, hasSecret := secret[id]
		if hasData && hasSecret && len(d) != len(sec) {
			diags.AddAttributeError(stepsPath, "Invalid steps", fmt.Sprintf(
				"Step %q has %d forms in steps but %d in sensitive_steps; give both the same number of forms, "+
					"using `{}` for a form that has no secret fields.", id, len(d), len(sec)))
			continue
		}
		for i := range max(len(d), len(sec)) {
			step := client.FlowStep{StepID: id}
			if hasData {
				step.Data = d[i]
			}
			if hasSecret {
				step.SensitiveData = sec[i]
			}
			out = append(out, step)
		}
	}
	return out, diags
}

// decodeSteps decodes an object from step_id to the fields of the form, or to a list of such
// objects for a step_id that is shown several times; null decodes to nil. A step given as an
// object decodes to a list with one entry.
func decodeSteps(v dyntype.Value) (map[string][]map[string]any, error) {
	if v.IsNull() || v.IsUnderlyingValueNull() {
		return nil, nil
	}
	raw, err := v.JSON()
	if err != nil {
		return nil, err
	}
	var byID map[string]json.RawMessage
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, fmt.Errorf("must be an object from step_id to an object of fields or a list of such objects: %w", err)
	}
	out := make(map[string][]map[string]any, len(byID))
	for id, rawStep := range byID {
		if bytes.HasPrefix(bytes.TrimSpace(rawStep), []byte("[")) {
			var forms []map[string]any
			if err := json.Unmarshal(rawStep, &forms); err != nil {
				return nil, fmt.Errorf("step %q must be a list of objects of fields: %w", id, err)
			}
			if len(forms) == 0 {
				return nil, fmt.Errorf("step %q must have at least one form", id)
			}
			out[id] = forms
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(rawStep, &fields); err != nil {
			return nil, fmt.Errorf("step %q must be an object of fields or a list of such objects: %w", id, err)
		}
		out[id] = []map[string]any{fields}
	}
	return out, nil
}

// setEntry copies what HA reports about the config entry into m.
func (m *integrationModel) setEntry(e client.ConfigEntry) {
	m.set(e)
	m.Domain = types.StringValue(e.Domain)
}

func (r *integrationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	steps, diags := flowSteps(plan.Steps, plan.SensitiveSteps)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	domain := plan.Domain.ValueString()
	entryID, err := r.flows.Run(ctx, domain, "", steps)
	if err != nil {
		resp.Diagnostics.AddError("Creating config entry", client.ErrorDetail(err))
		return
	}

	entry, ok, err := r.entries.Get(ctx, domain, entryID)
	if err == nil && !ok {
		err = fmt.Errorf("config entry %s of %s was created but is gone", entryID, domain)
	}
	if err != nil {
		// Created but not read: keep it in state, so it is tainted and replaced, not orphaned.
		plan.unread(entryID)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
		resp.Diagnostics.AddError("Reading the new config entry", client.ErrorDetail(err))
		return
	}
	plan.setEntry(entry)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *integrationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// After an import the domain is unknown, so look at every entry.
	entry, ok, err := r.entries.Get(ctx, state.Domain.ValueString(), state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading config entry", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	// The steps cannot be read back, so they stay as they are.
	state.setEntry(entry)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update only records the steps after an import: every other change replaces the entry.
func (r *integrationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan integrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// restartWarning is the detail of the warning when HA needs a restart to finish deleting a config
// entry.
const restartWarning = "The config entry was deleted, but Home Assistant needs a restart to finish " +
	"removing the integration."

func (r *integrationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state integrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	restart, err := r.entries.Delete(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Deleting config entry", client.ErrorDetail(err))
		return
	}
	if restart {
		resp.Diagnostics.AddWarning("Restart Home Assistant", restartWarning)
	}
}

func (r *integrationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// stepsValidator checks at plan time that `steps` or `sensitive_steps` is an object whose
// values are objects, or lists of objects for a step that is shown several times. `steps` must
// have at least one step.
type stepsValidator struct{ sensitive bool }

var _ validator.Dynamic = stepsValidator{}

func (stepsValidator) Description(context.Context) string {
	return "must be an object from step_id to an object of fields, or to a list of such objects"
}

func (v stepsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v stepsValidator) ValidateDynamic(ctx context.Context, req validator.DynamicRequest, resp *validator.DynamicResponse) {
	cv := req.ConfigValue
	if cv.IsNull() || cv.IsUnknown() || cv.IsUnderlyingValueNull() || cv.IsUnderlyingValueUnknown() {
		return
	}
	steps, ok := objectElements(cv.UnderlyingValue())
	if !ok {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps",
			fmt.Sprintf("Must be an object from step_id to an object of fields, got %s.", cv.UnderlyingValue().Type(ctx)))
		return
	}
	if len(steps) == 0 && !v.sensitive {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps",
			"At least one step is required, e.g. `{ user = {} }`.")
	}
	for id, fields := range steps {
		fields = unwrapDynamic(fields)
		if fields == nil || fields.IsNull() || fields.IsUnknown() {
			continue
		}
		if forms, ok := listElements(fields); ok {
			if len(forms) == 0 {
				resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps",
					fmt.Sprintf("Step %q must have at least one form.", id))
			}
			for i, form := range forms {
				form = unwrapDynamic(form)
				if form == nil || form.IsNull() || form.IsUnknown() {
					continue
				}
				if _, ok := objectElements(form); !ok {
					detail := fmt.Sprintf("Form %d of step %q must be an object of fields, got %s.", i+1, id, form.Type(ctx))
					if v.sensitive {
						detail = fmt.Sprintf("Form %d of step %q must be an object of fields.", i+1, id)
					}
					resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps", detail)
				}
			}
			continue
		}
		if _, ok := objectElements(fields); !ok {
			detail := fmt.Sprintf("Step %q must be an object of fields or a list of such objects, got %s.", id, fields.Type(ctx))
			if v.sensitive {
				detail = fmt.Sprintf("Step %q must be an object of fields or a list of such objects.", id)
			}
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps", detail)
		}
	}
}

func unwrapDynamic(v attr.Value) attr.Value {
	if dv, ok := v.(basetypes.DynamicValue); ok {
		return dv.UnderlyingValue()
	}
	return v
}

// listElements returns the elements of a tuple or list.
func listElements(v attr.Value) ([]attr.Value, bool) {
	switch u := v.(type) {
	case basetypes.TupleValue:
		return u.Elements(), true
	case basetypes.ListValue:
		return u.Elements(), true
	}
	return nil, false
}

// objectElements returns the attributes of an object or the elements of a map.
func objectElements(v attr.Value) (map[string]attr.Value, bool) {
	switch u := v.(type) {
	case basetypes.ObjectValue:
		return u.Attributes(), true
	case basetypes.MapValue:
		return u.Elements(), true
	}
	return nil, false
}
