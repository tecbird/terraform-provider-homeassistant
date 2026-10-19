package resources

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

var (
	_ resource.ResourceWithConfigure   = (*defaultDashboardResource)(nil)
	_ resource.ResourceWithImportState = (*defaultDashboardResource)(nil)
)

const (
	// defaultDashboardID is the `id` and the import ID of the singleton.
	defaultDashboardID = "default"
	// legacyDefaultURLPath is the url_path under which recent Home Assistant releases list a
	// default dashboard that has a saved config (or that runs in YAML mode).
	legacyDefaultURLPath = "lovelace"
)

// NewDefaultDashboard returns the `homeassistant_default_dashboard` resource.
func NewDefaultDashboard() resource.Resource {
	return &defaultDashboardResource{}
}

type defaultDashboardResource struct {
	dashboards client.Dashboards
}

type defaultDashboardModel struct {
	ID     types.String  `tfsdk:"id"`
	Config dyntype.Value `tfsdk:"config"`
}

func (r *defaultDashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_default_dashboard"
}

func (r *defaultDashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The config of the built-in default dashboard (the \"Overview\" dashboard, which has no " +
			"`url_path`). There is only one per Home Assistant instance. Its title, icon, and sidebar " +
			"visibility are not managed. Destroying the resource deletes the saved config, so Home " +
			"Assistant generates the dashboard automatically again.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always `" + defaultDashboardID + "`. Import with this ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"config": schema.DynamicAttribute{
				Description: "The full Lovelace config (`views`, …), as an HCL object or " +
					"`yamldecode(file(\"dashboard.yaml\"))`. YAML copied from the raw configuration " +
					"editor works unchanged. Home Assistant validates nothing here; the frontend " +
					"shows errors in cards it cannot render. Creating the resource fails if the " +
					"default dashboard already has a saved config: import it first.",
				CustomType: dyntype.Type{},
				Required:   true,
			},
		},
	}
}

func (r *defaultDashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.dashboards = client.NewDashboards(c.WS)
}

func (r *defaultDashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan defaultDashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.create(ctx, plan.Config, resp.Private, &resp.Diagnostics) {
		return
	}
	plan.ID = types.StringValue(defaultDashboardID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// create saves config as the default dashboard's config, unless it already has one. It reports
// whether it saved.
func (r *defaultDashboardResource) create(ctx context.Context, config dyntype.Value, private privateSetter, diags *diag.Diagnostics) bool {
	// Like the config items (ADR-0024), create never replaces a config that is already there.
	_, err := r.dashboards.Config(ctx, client.DefaultDashboardURLPath)
	switch {
	case err == nil:
		diags.AddError("The default dashboard already has a config",
			"Creating would replace it. Import it with `tofu import` and ID \""+defaultDashboardID+
				"\", or delete its config in Home Assistant first.")
		return false
	case !errors.Is(err, client.ErrNoDashboardConfig):
		diags.AddError("Reading the default dashboard", client.ErrorDetail(err))
		return false
	}
	if !r.checkStorageMode(ctx, diags) {
		return false
	}
	return r.save(ctx, config, private, diags)
}

// save replaces the default dashboard's config and records the stored baseline (ADR-0023). It
// reports whether the config was saved.
func (r *defaultDashboardResource) save(ctx context.Context, config dyntype.Value, private privateSetter, diags *diag.Diagnostics) bool {
	raw, err := config.JSON()
	if err != nil {
		diags.AddAttributeError(path.Root("config"), "Invalid dashboard config", err.Error())
		return false
	}
	if err := r.dashboards.SaveConfig(ctx, client.DefaultDashboardURLPath, raw); err != nil {
		addSaveError(diags, "Saving the default dashboard config", err)
		return false
	}
	storeDashboardBaseline(ctx, r.dashboards, client.DefaultDashboardURLPath, private, diags)
	return true
}

func (r *defaultDashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state defaultDashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.checkStorageMode(ctx, &resp.Diagnostics) {
		return
	}

	// An auto-generated dashboard has no saved config. It is left null, so the next apply saves
	// the configured config.
	config := dyntype.NewNull()
	raw, err := r.dashboards.Config(ctx, client.DefaultDashboardURLPath)
	switch {
	case errors.Is(err, client.ErrNoDashboardConfig):
	case err != nil:
		resp.Diagnostics.AddError("Reading the default dashboard config", client.ErrorDetail(err))
		return
	default:
		baseline, diags := storedBaseline(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		config, err = refreshedConfig(state.Config, raw, baseline)
		if err != nil {
			resp.Diagnostics.AddError("Reading the default dashboard config", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, defaultDashboardModel{
		ID:     types.StringValue(defaultDashboardID),
		Config: config,
	})...)
}

func (r *defaultDashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan defaultDashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.save(ctx, plan.Config, resp.Private, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *defaultDashboardResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	err := r.dashboards.DeleteConfig(ctx, client.DefaultDashboardURLPath)
	var wsErr *client.WSError
	if errors.As(err, &wsErr) && wsErr.Code == "config_not_found" {
		return // already auto-generated
	}
	if err != nil {
		resp.Diagnostics.AddError("Deleting the default dashboard config", client.ErrorDetail(err))
	}
}

func (r *defaultDashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != defaultDashboardID {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("The default dashboard is imported with the ID %q, got %q.", defaultDashboardID, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// checkStorageMode reports an error and returns false if the default dashboard runs in YAML
// mode, where its config is a file that Home Assistant does not let anyone save over the API.
// Releases that list the default dashboard under legacyDefaultURLPath show its mode there; a
// default dashboard without saved config is not listed and is always storage mode.
func (r *defaultDashboardResource) checkStorageMode(ctx context.Context, diags *diag.Diagnostics) bool {
	dash, ok, err := r.dashboards.Get(ctx, legacyDefaultURLPath)
	switch {
	case err != nil:
		diags.AddError("Reading the default dashboard", client.ErrorDetail(err))
		return false
	case ok && dash.Mode != "storage":
		diags.AddError("Unsupported dashboard",
			fmt.Sprintf("The default dashboard is in %s mode. Only storage-mode dashboards can be managed.", dash.Mode))
		return false
	}
	return true
}
