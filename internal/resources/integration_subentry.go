package resources

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

var (
	_ resource.ResourceWithConfigure   = (*integrationSubentryResource)(nil)
	_ resource.ResourceWithImportState = (*integrationSubentryResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*integrationSubentryResource)(nil)
)

// NewIntegrationSubentry returns the `homeassistant_integration_subentry` resource.
func NewIntegrationSubentry() resource.Resource {
	return &integrationSubentryResource{}
}

type integrationSubentryResource struct {
	subentries client.ConfigSubentries
}

type integrationSubentryModel struct {
	ID                  types.String  `tfsdk:"id"`
	EntryID             types.String  `tfsdk:"entry_id"`
	SubentryType        types.String  `tfsdk:"subentry_type"`
	Steps               dyntype.Value `tfsdk:"steps"`
	SensitiveSteps      dyntype.Value `tfsdk:"sensitive_steps"`
	SubentryID          types.String  `tfsdk:"subentry_id"`
	Title               types.String  `tfsdk:"title"`
	UniqueID            types.String  `tfsdk:"unique_id"`
	SupportsReconfigure types.Bool    `tfsdk:"supports_reconfigure"`
}

// subentryIDSeparator separates the entry_id and the subentry_id in the resource ID.
const subentryIDSeparator = "/"

var supportsReconfigurePath = path.Root("supports_reconfigure")

func (r *integrationSubentryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration_subentry"
}

func (r *integrationSubentryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceUnlessReconfigurable := dynamicplanmodifier.RequiresReplaceIf(subentryStepsChanged,
		"Changing the steps reconfigures the subentry if its type supports that, and replaces it "+
			"otherwise; right after an import, the steps are only recorded.",
		"Changing the steps reconfigures the subentry if its type supports that, and replaces it "+
			"otherwise; right after an import, the steps are only recorded.")
	resp.Schema = schema.Schema{
		Description: "A subentry of a config entry, e.g. a plane of `forecast_solar` or a " +
			"conversation agent of an AI integration, created by answering its subentry flow with " +
			"the given steps. Home Assistant cannot return the data a subentry was created with, " +
			"so the steps are never compared with Home Assistant, and only a subentry that is gone " +
			"is detected as drift. Changing the steps runs the subentry's reconfigure flow, which " +
			"keeps the subentry with its devices and entities, if its type has one " +
			"(`supports_reconfigure`); otherwise it replaces the subentry. Flows that need a menu " +
			"are not supported.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "`<entry_id>/<subentry_id>`. Import with this ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"entry_id": schema.StringAttribute{
				Description: "The `entry_id` of the config entry the subentry belongs to, e.g. " +
					"`homeassistant_integration.solar.id`. Changing it replaces the subentry.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"subentry_type": schema.StringAttribute{
				Description: "The kind of subentry, as the integration names it, e.g. `plane` " +
					"for `forecast_solar` or `conversation` for an AI integration. Changing it " +
					"replaces the subentry.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"steps": schema.DynamicAttribute{
				Description: "The answers to the subentry flow's forms, as an object from `step_id` " +
					"to the form's fields, e.g. `{ user = { declination = 30 } }`. They work as " +
					"the `steps` of `homeassistant_integration` do. If Home Assistant asks for a " +
					"step that is not given, or rejects the data, the flow is aborted and nothing " +
					"is created. When the steps change and the type supports it, the reconfigure " +
					"flow answers each of its forms from the steps by the form's fields: a field " +
					"takes its value from the step with the form's `step_id`, else from any step " +
					"that has it, and fields the form does not show are left out. Otherwise a " +
					"change replaces the subentry. Right after an import the steps are only recorded. " +
					"Creating the subentry needs them or `sensitive_steps`. An imported subentry " +
					"can do without both: they stay null until set, which is what `tofu plan " +
					"-generate-config-out` writes, and removing both later only forgets them.",
				CustomType:    dyntype.Type{},
				Optional:      true,
				Validators:    []validator.Dynamic{stepsValidator{}},
				PlanModifiers: []planmodifier.Dynamic{replaceUnlessReconfigurable},
			},
			"sensitive_steps": schema.DynamicAttribute{
				Description: "More fields, in the same shape as `steps`, that are hidden in plan " +
					"output, e.g. `{ user = { api_key = var.api_key } }`. They are merged into " +
					"the step of the same `step_id`; on a field in both, this one wins. They are " +
					"still stored in the state.",
				CustomType:    dyntype.Type{},
				Optional:      true,
				Sensitive:     true,
				Validators:    []validator.Dynamic{stepsValidator{sensitive: true}},
				PlanModifiers: []planmodifier.Dynamic{replaceUnlessReconfigurable},
			},
			"subentry_id": computedString("The subentry's `subentry_id`, assigned by Home Assistant."),
			"title":       computedString("Title of the subentry, chosen by the integration's flow."),
			"unique_id": computedString("The subentry's unique ID within its config entry, if the " +
				"integration sets one; otherwise null."),
			"supports_reconfigure": schema.BoolAttribute{
				Description: "Whether subentries of this type have a reconfigure flow, as Home " +
					"Assistant reports it. If so, changed steps reconfigure the subentry in place; " +
					"if not, they replace it.",
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

// subentryStepsChanged requires replacement for changed steps, unless they are only recorded
// after an import or forgotten, or the subentry is reconfigured instead.
func subentryStepsChanged(ctx context.Context, req planmodifier.DynamicRequest, resp *dynamicplanmodifier.RequiresReplaceIfFuncResponse) {
	var prior, priorSecret, planned, plannedSecret dyntype.Value
	var reconfigurable types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, stepsPath, &prior)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, sensitiveStepsPath, &priorSecret)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, stepsPath, &planned)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, sensitiveStepsPath, &plannedSecret)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, supportsReconfigurePath, &reconfigurable)...)
	resp.RequiresReplace = !noSteps(prior, priorSecret) && !noSteps(planned, plannedSecret) && !reconfigurable.ValueBool()
}

var sensitiveStepsPath = path.Root("sensitive_steps")

// noSteps reports whether neither `steps` nor `sensitive_steps` is set, which means the provider
// does not know the steps: in state after an import or after both were removed from the
// configuration, in a plan when both are left out. The steps cannot be read back, so the first
// ones written are recorded, not applied, and removing them only forgets them. Either attribute
// alone is enough to know them (ADR-0021).
func noSteps(steps, sensitive dyntype.Value) bool {
	return nullSteps(steps) && nullSteps(sensitive)
}

// nullSteps reports whether a step object is null.
func nullSteps(steps dyntype.Value) bool {
	return steps.IsNull() || steps.IsUnderlyingValueNull()
}

// reconfigures reports whether updating state to plan runs the reconfigure flow: the steps change,
// they are not only being recorded after an import or forgotten, and the type has a reconfigure
// flow.
func reconfigures(state, plan integrationSubentryModel) bool {
	if noSteps(state.Steps, state.SensitiveSteps) || noSteps(plan.Steps, plan.SensitiveSteps) ||
		!state.SupportsReconfigure.ValueBool() {
		return false
	}
	return !stepsEqual(state.Steps, plan.Steps) || !stepsEqual(state.SensitiveSteps, plan.SensitiveSteps)
}

// stepsEqual reports whether two step objects hold the same steps. A value that is not known yet
// counts as different; null and an empty object count as equal.
func stepsEqual(a, b dyntype.Value) bool {
	if a.IsUnknown() || b.IsUnknown() || a.IsUnderlyingValueUnknown() || b.IsUnderlyingValueUnknown() {
		return false
	}
	da, errA := decodeSteps(a)
	db, errB := decodeSteps(b)
	if errA != nil || errB != nil {
		return false
	}
	if len(da) == 0 && len(db) == 0 {
		return true
	}
	return reflect.DeepEqual(da, db)
}

// ModifyPlan fails a plan that creates the subentry without steps, and marks the title and unique
// ID unknown when the update reconfigures the subentry, because the reconfigure flow may change
// them.
func (r *integrationSubentryResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		requireSubentrySteps(ctx, req.Plan, resp)
		return
	}
	var state, plan integrationSubentryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !reconfigures(state, plan) {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("title"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("unique_id"), types.StringUnknown())...)
}

// requireSubentrySteps fails a plan that creates the subentry without steps, since only an
// imported subentry can do without them. A replacement is planned as a create too, so this covers
// a changed `entry_id` or `subentry_type` and `-replace`.
func requireSubentrySteps(ctx context.Context, plan tfsdk.Plan, resp *resource.ModifyPlanResponse) {
	var steps, sensitive dyntype.Value
	resp.Diagnostics.Append(plan.GetAttribute(ctx, stepsPath, &steps)...)
	resp.Diagnostics.Append(plan.GetAttribute(ctx, sensitiveStepsPath, &sensitive)...)
	if !resp.Diagnostics.HasError() && noSteps(steps, sensitive) {
		resp.Diagnostics.AddAttributeError(stepsPath, "Missing steps", "Creating the subentry runs "+
			"its subentry flow, which needs `steps` or `sensitive_steps`, e.g. "+
			"`{ user = { declination = 30 } }`. Only a subentry imported from Home Assistant can do "+
			"without them, and replacing it creates a new one.")
	}
}

func (r *integrationSubentryResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.subentries = client.NewConfigSubentries(c.WS, c.REST)
}

// set copies what HA reports about the subentry into m.
func (m *integrationSubentryModel) set(s client.ConfigSubentry) {
	m.SubentryID = types.StringValue(s.SubentryID)
	m.ID = types.StringValue(m.EntryID.ValueString() + subentryIDSeparator + s.SubentryID)
	m.SubentryType = types.StringValue(s.SubentryType)
	m.Title = types.StringValue(s.Title)
	m.UniqueID = types.StringPointerValue(s.UniqueID)
}

func (r *integrationSubentryResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationSubentryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	steps, diags := flowSteps(plan.Steps, plan.SensitiveSteps)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	entryID, subentryType := plan.EntryID.ValueString(), plan.SubentryType.ValueString()
	sub, err := r.subentries.Create(ctx, entryID, subentryType, steps)
	if err != nil {
		resp.Diagnostics.AddError("Creating config subentry", subentryErrorDetail(err))
		return
	}
	plan.set(sub)
	plan.SupportsReconfigure = types.BoolNull()
	if ok, err := r.subentries.SupportsReconfigure(ctx, entryID, subentryType); err == nil {
		plan.SupportsReconfigure = types.BoolValue(ok)
	} else {
		// The subentry exists. The next refresh reads the flag; until then, changes replace it.
		resp.Diagnostics.AddWarning("Reading whether the subentry can be reconfigured", client.ErrorDetail(err))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *integrationSubentryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationSubentryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	entryID := state.EntryID.ValueString()
	sub, ok, err := r.subentries.Get(ctx, entryID, state.SubentryID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading config subentry", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	reconfigurable, err := r.subentries.SupportsReconfigure(ctx, entryID, sub.SubentryType)
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading config subentry", client.ErrorDetail(err))
		return
	}
	// The steps cannot be read back, so they stay as they are.
	state.set(sub)
	state.SupportsReconfigure = types.BoolValue(reconfigurable)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update runs the reconfigure flow for changed steps, or only records them after an import. Types
// without a reconfigure flow are replaced instead (subentryStepsChanged).
func (r *integrationSubentryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state integrationSubentryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if reconfigures(state, plan) {
		steps, diags := flowSteps(plan.Steps, plan.SensitiveSteps)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		sub, err := r.subentries.Reconfigure(ctx, state.EntryID.ValueString(), state.SubentryType.ValueString(),
			state.SubentryID.ValueString(), steps)
		if err != nil {
			resp.Diagnostics.AddError("Reconfiguring config subentry", reconfigureErrorDetail(err))
			return
		}
		plan.set(sub)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *integrationSubentryResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state integrationSubentryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.subentries.Delete(ctx, state.EntryID.ValueString(), state.SubentryID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Deleting config subentry", client.ErrorDetail(err))
	}
}

func (r *integrationSubentryResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	entryID, subentryID, err := parseSubentryID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("entry_id"), entryID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("subentry_id"), subentryID)...)
}

// parseSubentryID splits `<entry_id>/<subentry_id>`.
func parseSubentryID(id string) (entryID, subentryID string, err error) {
	entryID, subentryID, ok := strings.Cut(id, subentryIDSeparator)
	if !ok || entryID == "" || subentryID == "" || strings.Contains(subentryID, subentryIDSeparator) {
		return "", "", fmt.Errorf("expected `<entry_id>%s<subentry_id>`, got %q", subentryIDSeparator, id)
	}
	return entryID, subentryID, nil
}

// subentryErrorDetail is client.ErrorDetail with hints that fit creating a subentry.
func subentryErrorDetail(err error) string {
	var unidentified *client.SubentryUnidentifiedError
	if errors.As(err, &unidentified) {
		detail := err.Error() + fmt.Sprintf("\n\nThe subentry exists in Home Assistant, but not in the "+
			"state. Do not apply again: that would create a second one. Import it instead: list the "+
			"subentries of config entry %s with the WebSocket command `config_entries/subentries/list`, "+
			"find the one of type %q titled %q, and run `tofu import <resource address> %s/<subentry_id>`.",
			unidentified.EntryID, unidentified.SubentryType, unidentified.Title, unidentified.EntryID)
		if len(unidentified.Candidates) > 0 {
			detail += " Candidates: " + strings.Join(unidentified.Candidates, ", ") + "."
		}
		return detail
	}
	var aborted *client.FlowAbortedError
	if errors.As(err, &aborted) && aborted.Reason == "already_configured" {
		return err.Error() + "\n\nThe config entry already has this subentry. Import it into this " +
			"resource with `tofu import <resource address> <entry_id>/<subentry_id>`, or delete it " +
			"in Home Assistant."
	}
	var unknown *client.SubentryTypeUnknownError
	if errors.As(err, &unknown) {
		return err.Error() + "\n\nCheck `subentry_type` against the subentry types the integration " +
			"offers."
	}
	if errors.Is(err, client.ErrNotFound) {
		return err.Error() + "\n\nCheck `entry_id`: the config entry does not exist."
	}
	return client.ErrorDetail(err)
}

// reconfigureErrorDetail is client.ErrorDetail with hints that fit reconfiguring a subentry.
func reconfigureErrorDetail(err error) string {
	var ambiguous *client.FlowFieldAmbiguousError
	if errors.As(err, &ambiguous) {
		return err.Error() + "\n\nThe subentry was not changed. Add the field to a step named after " +
			"the reconfigure form's `step_id`, so it is clear which value applies."
	}
	if errors.Is(err, client.ErrNotFound) {
		return err.Error() + "\n\nThe subentry is gone. The next plan creates it again."
	}
	return client.ErrorDetail(err)
}
