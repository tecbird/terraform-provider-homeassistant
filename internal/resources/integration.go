package resources

import (
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
	_ resource.ResourceWithModifyPlan  = (*integrationResource)(nil)
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

var (
	stepsPath          = path.Root("steps")
	sensitiveStepsPath = path.Root("sensitive_steps")
)

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
				"is answered by `{}`. If Home Assistant asks for a step that is not given, or " +
				"rejects the data, the flow is aborted and nothing is created. Changing the steps " +
				"replaces the entry, except right after an import, when they are only recorded. " +
				"Creating the entry needs a step, here or in `sensitive_steps`. An imported entry " +
				"can do without them: they stay null until set, which is what `tofu plan " +
				"-generate-config-out` writes, and removing both later only forgets them.",
			CustomType:    dyntype.Type{},
			Optional:      true,
			Validators:    []validator.Dynamic{stepsValidator{}},
			PlanModifiers: []planmodifier.Dynamic{replaceUnlessImported},
		},
		"sensitive_steps": schema.DynamicAttribute{
			Description: "More fields, in the same shape as `steps`, that are hidden in plan " +
				"output, e.g. `{ user = { password = var.password } }`. They are merged into the " +
				"step of the same `step_id`; on a field in both, this one wins. They are still " +
				"stored in the state.",
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

// stepsChanged requires replacement unless the prior or the planned steps are unknown to the
// provider, i.e. neither `steps` nor `sensitive_steps` holds a step (hasSteps). That is so in
// state after an import, or after both were removed from the configuration. The steps cannot be
// read back, so the first ones written are recorded, not applied, and removing them only forgets
// them.
func stepsChanged(ctx context.Context, req planmodifier.DynamicRequest, resp *dynamicplanmodifier.RequiresReplaceIfFuncResponse) {
	var prior, priorSecret, planned, plannedSecret dyntype.Value
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, stepsPath, &prior)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, sensitiveStepsPath, &priorSecret)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, stepsPath, &planned)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, sensitiveStepsPath, &plannedSecret)...)
	resp.RequiresReplace = hasSteps(prior, priorSecret) && hasSteps(planned, plannedSecret)
}

// hasSteps reports whether `steps` or `sensitive_steps` holds a step, as flowSteps merges both.
// A value that is not known yet counts as holding one; null and `{}` hold none.
func hasSteps(steps, sensitive dyntype.Value) bool {
	return holdsStep(steps) || holdsStep(sensitive)
}

// holdsStep reports whether one step object holds a step; see hasSteps.
func holdsStep(v dyntype.Value) bool {
	if v.IsNull() || v.IsUnderlyingValueNull() {
		return false
	}
	if v.IsUnknown() || v.IsUnderlyingValueUnknown() {
		return true
	}
	elems, ok := objectElements(v.UnderlyingValue())
	return !ok || len(elems) > 0
}

// ModifyPlan fails a plan that creates the entry without a step, since only an imported entry can
// do without them. A replacement is planned as a create too, so this covers a changed `domain` and
// `-replace`.
func (r *integrationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var steps, sensitive dyntype.Value
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, stepsPath, &steps)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, sensitiveStepsPath, &sensitive)...)
	if !resp.Diagnostics.HasError() && !hasSteps(steps, sensitive) {
		resp.Diagnostics.AddAttributeError(stepsPath, "Missing steps", missingStepsDetail)
	}
}

// missingStepsDetail explains why a create needs a step.
const missingStepsDetail = "Creating the config entry runs its config flow, which needs `steps` " +
	"or `sensitive_steps`, e.g. `{ user = { host = \"192.168.1.50\" } }`. Only an entry imported " +
	"from Home Assistant can do without them, and replacing it creates a new one."

func (r *integrationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.flows = client.NewConfigFlows(c.REST)
	r.entries = client.NewConfigEntries(c.WS, c.REST)
}

// flowSteps merges `steps` and `sensitive_steps` into the steps of the config-flow driver.
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
			"sensitive_steps must be an object of objects.")
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
		out = append(out, client.FlowStep{StepID: id, Data: data[id], SensitiveData: secret[id]})
	}
	return out, diags
}

// decodeSteps decodes an object of objects; null decodes to nil.
func decodeSteps(v dyntype.Value) (map[string]map[string]any, error) {
	if v.IsNull() || v.IsUnderlyingValueNull() {
		return nil, nil
	}
	raw, err := v.JSON()
	if err != nil {
		return nil, err
	}
	var m map[string]map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("must be an object from step_id to an object of fields: %w", err)
	}
	return m, nil
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
// values are objects. `steps` must have at least one step.
type stepsValidator struct{ sensitive bool }

var _ validator.Dynamic = stepsValidator{}

func (stepsValidator) Description(context.Context) string {
	return "must be an object from step_id to an object of fields"
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
		if dv, ok := fields.(basetypes.DynamicValue); ok {
			fields = dv.UnderlyingValue()
		}
		if fields == nil || fields.IsNull() || fields.IsUnknown() {
			continue
		}
		if _, ok := objectElements(fields); !ok {
			detail := fmt.Sprintf("Step %q must be an object of fields, got %s.", id, fields.Type(ctx))
			if v.sensitive {
				detail = fmt.Sprintf("Step %q must be an object of fields.", id)
			}
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid steps", detail)
		}
	}
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
