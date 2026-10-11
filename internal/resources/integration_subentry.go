package resources

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/dynamicplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

var (
	_ resource.ResourceWithConfigure   = (*integrationSubentryResource)(nil)
	_ resource.ResourceWithImportState = (*integrationSubentryResource)(nil)
)

// NewIntegrationSubentry returns the `homeassistant_integration_subentry` resource.
func NewIntegrationSubentry() resource.Resource {
	return &integrationSubentryResource{}
}

type integrationSubentryResource struct {
	subentries client.ConfigSubentries
}

type integrationSubentryModel struct {
	ID             types.String  `tfsdk:"id"`
	EntryID        types.String  `tfsdk:"entry_id"`
	SubentryType   types.String  `tfsdk:"subentry_type"`
	Steps          dyntype.Value `tfsdk:"steps"`
	SensitiveSteps dyntype.Value `tfsdk:"sensitive_steps"`
	SubentryID     types.String  `tfsdk:"subentry_id"`
	Title          types.String  `tfsdk:"title"`
	UniqueID       types.String  `tfsdk:"unique_id"`
}

// subentryIDSeparator separates the entry_id and the subentry_id in the resource ID.
const subentryIDSeparator = "/"

func (r *integrationSubentryResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration_subentry"
}

func (r *integrationSubentryResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceUnlessImported := dynamicplanmodifier.RequiresReplaceIf(stepsChanged,
		"Changing the steps replaces the subentry, except right after an import.",
		"Changing the steps replaces the subentry, except right after an import.")
	resp.Schema = schema.Schema{
		Description: "A subentry of a config entry, e.g. a plane of `forecast_solar` or a " +
			"conversation agent of an AI integration, created by answering its subentry flow with " +
			"the given steps. Home Assistant cannot return the data a subentry was created with, " +
			"so the steps are never compared with Home Assistant: changing them replaces the " +
			"subentry, and only a subentry that is gone is detected as drift. Flows that need a " +
			"menu are not supported.",
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
					"is created. Changing the steps replaces the subentry, except right after an " +
					"import, when they are only recorded.",
				CustomType:    dyntype.Type{},
				Required:      true,
				Validators:    []validator.Dynamic{stepsValidator{}},
				PlanModifiers: []planmodifier.Dynamic{replaceUnlessImported},
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
				PlanModifiers: []planmodifier.Dynamic{replaceUnlessImported},
			},
			"subentry_id": computedString("The subentry's `subentry_id`, assigned by Home Assistant."),
			"title":       computedString("Title of the subentry, chosen by the integration's flow."),
			"unique_id": computedString("The subentry's unique ID within its config entry, if the " +
				"integration sets one; otherwise null."),
		},
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

	sub, err := r.subentries.Create(ctx, plan.EntryID.ValueString(), plan.SubentryType.ValueString(), steps)
	if err != nil {
		resp.Diagnostics.AddError("Creating config subentry", subentryErrorDetail(err))
		return
	}
	plan.set(sub)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *integrationSubentryResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationSubentryModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sub, ok, err := r.subentries.Get(ctx, state.EntryID.ValueString(), state.SubentryID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading config subentry", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	// The steps cannot be read back, so they stay as they are.
	state.set(sub)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// Update only records the steps after an import: every other change replaces the subentry.
func (r *integrationSubentryResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan integrationSubentryModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
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

// subentryErrorDetail is client.ErrorDetail with hints that fit subentries.
func subentryErrorDetail(err error) string {
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
