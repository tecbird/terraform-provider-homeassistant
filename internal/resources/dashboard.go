package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

var (
	_ resource.ResourceWithConfigure   = (*dashboardResource)(nil)
	_ resource.ResourceWithImportState = (*dashboardResource)(nil)
)

// NewDashboard returns the `homeassistant_dashboard` resource.
func NewDashboard() resource.Resource {
	return &dashboardResource{}
}

type dashboardResource struct {
	dashboards client.Dashboards
}

type dashboardModel struct {
	ID            types.String  `tfsdk:"id"`
	URLPath       types.String  `tfsdk:"url_path"`
	Title         types.String  `tfsdk:"title"`
	Icon          types.String  `tfsdk:"icon"`
	ShowInSidebar types.Bool    `tfsdk:"show_in_sidebar"`
	RequireAdmin  types.Bool    `tfsdk:"require_admin"`
	Config        dyntype.Value `tfsdk:"config"`
	DashboardID   types.String  `tfsdk:"dashboard_id"`
}

func (m dashboardModel) meta() client.DashboardMeta {
	return client.DashboardMeta{
		Title:         m.Title.ValueString(),
		Icon:          m.Icon.ValueStringPointer(),
		ShowInSidebar: m.ShowInSidebar.ValueBool(),
		RequireAdmin:  m.RequireAdmin.ValueBool(),
	}
}

// metaChanged reports whether the metadata differs, i.e. whether `lovelace/dashboards/update`
// is needed.
func (m dashboardModel) metaChanged(o dashboardModel) bool {
	return !m.Title.Equal(o.Title) || !m.Icon.Equal(o.Icon) ||
		!m.ShowInSidebar.Equal(o.ShowInSidebar) || !m.RequireAdmin.Equal(o.RequireAdmin)
}

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A storage-mode Lovelace dashboard: its metadata and its whole config. Reuse " +
			"sections and cards across dashboards with `locals`, module outputs, and `for` expressions.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The `url_path`. Import with this ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"url_path": schema.StringAttribute{
				Description: "URL segment of the dashboard, e.g. `my-home`. It must contain a hyphen. " +
					"Changing it replaces the dashboard.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{urlPathValidator},
			},
			"title": schema.StringAttribute{
				Description: "Title shown in the sidebar.",
				Required:    true,
			},
			"icon": schema.StringAttribute{
				Description: "Sidebar icon, e.g. `mdi:home-variant`.",
				Optional:    true,
			},
			"show_in_sidebar": schema.BoolAttribute{
				Description: "Whether the dashboard is listed in the sidebar. Defaults to `true`.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
			},
			"require_admin": schema.BoolAttribute{
				Description: "Whether only admin users can see the dashboard. Defaults to `false`.",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"config": schema.DynamicAttribute{
				Description: "The full Lovelace config (`views`, …), as an HCL object or " +
					"`yamldecode(file(\"dashboard.yaml\"))`. YAML copied from the raw configuration " +
					"editor works unchanged. Home Assistant validates nothing here; the frontend " +
					"shows errors in cards it cannot render.",
				CustomType: dyntype.Type{},
				Required:   true,
			},
			"dashboard_id": schema.StringAttribute{
				Description: "Home Assistant's internal ID of the dashboard, `slugify(url_path)`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *dashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.dashboards = client.NewDashboards(c.WS)
}

func (r *dashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, err := plan.Config.JSON()
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("config"), "Invalid dashboard config", err.Error())
		return
	}

	urlPath := plan.URLPath.ValueString()
	dash, err := r.dashboards.Create(ctx, urlPath, plan.meta())
	if err != nil {
		resp.Diagnostics.AddError("Creating dashboard", client.ErrorDetail(err))
		return
	}
	if err := r.dashboards.SaveConfig(ctx, urlPath, config); err != nil {
		// Roll back, so a failed create leaves nothing behind that blocks the next attempt. The
		// rollback runs even if ctx is cancelled, e.g. by Ctrl-C.
		if delErr := r.dashboards.Delete(context.WithoutCancel(ctx), dash.ID); delErr != nil {
			err = errors.Join(err, fmt.Errorf("deleting the half-created dashboard %q: %w", urlPath, delErr))
		}
		addSaveError(&resp.Diagnostics, "Saving dashboard config", err)
		return
	}
	storeDashboardBaseline(ctx, r.dashboards, urlPath, resp.Private, &resp.Diagnostics)

	plan.ID = plan.URLPath
	plan.DashboardID = types.StringValue(dash.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	urlPath := state.URLPath.ValueString()
	dash, ok, err := r.dashboards.Get(ctx, urlPath)
	if err != nil {
		resp.Diagnostics.AddError("Reading dashboard", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	if dash.Mode != "storage" {
		resp.Diagnostics.AddError("Unsupported dashboard",
			fmt.Sprintf("Dashboard %q is in %s mode. Only storage-mode dashboards can be managed.", urlPath, dash.Mode))
		return
	}

	config := dyntype.NewNull()
	raw, err := r.dashboards.Config(ctx, urlPath)
	switch {
	case errors.Is(err, client.ErrNoDashboardConfig):
		// Left null, so the next apply saves the configured config.
	case err != nil:
		resp.Diagnostics.AddError("Reading dashboard config", client.ErrorDetail(err))
		return
	default:
		baseline, diags := storedBaseline(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		config, err = refreshedConfig(state.Config, raw, baseline)
		if err != nil {
			resp.Diagnostics.AddError("Reading dashboard config", err.Error())
			return
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, dashboardModel{
		ID:            types.StringValue(dash.URLPath),
		URLPath:       types.StringValue(dash.URLPath),
		Title:         types.StringValue(dash.Title),
		Icon:          types.StringPointerValue(dash.Icon),
		ShowInSidebar: types.BoolValue(dash.ShowInSidebar),
		RequireAdmin:  types.BoolValue(dash.RequireAdmin),
		Config:        config,
		DashboardID:   types.StringValue(dash.ID),
	})...)
}

func (r *dashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.metaChanged(state) {
		if err := r.dashboards.Update(ctx, state.DashboardID.ValueString(), plan.meta()); err != nil {
			resp.Diagnostics.AddError("Updating dashboard", client.ErrorDetail(err))
			return
		}
	}
	if !plan.Config.Equal(state.Config) {
		config, err := plan.Config.JSON()
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("config"), "Invalid dashboard config", err.Error())
			return
		}
		urlPath := plan.URLPath.ValueString()
		if err := r.dashboards.SaveConfig(ctx, urlPath, config); err != nil {
			addSaveError(&resp.Diagnostics, "Saving dashboard config", err)
			return
		}
		storeDashboardBaseline(ctx, r.dashboards, urlPath, resp.Private, &resp.Diagnostics)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// storeDashboardBaseline reads the config just saved back from HA and stores it as the baseline
// (ADR-0023). Failing to read it back is only a warning: without a baseline, Read falls back to
// semantic equality. It serves the default dashboard too, with client.DefaultDashboardURLPath.
func storeDashboardBaseline(ctx context.Context, dashboards client.Dashboards, urlPath string, private privateSetter, diags *diag.Diagnostics) {
	stored, err := dashboards.Config(ctx, urlPath)
	if err != nil {
		diags.AddWarning("Reading the dashboard config back",
			client.ErrorDetail(err)+"\n\nThe config was saved. Until the next apply, changes that "+
				"Home Assistant made on save may show as a difference.")
		return
	}
	diags.Append(storeBaseline(ctx, private, stored)...)
}

func (r *dashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.dashboards.Delete(ctx, state.DashboardID.ValueString())
	var wsErr *client.WSError
	if errors.As(err, &wsErr) && wsErr.Code == "not_found" {
		return // already gone
	}
	if err != nil {
		resp.Diagnostics.AddError("Deleting dashboard", client.ErrorDetail(err))
	}
}

func (r *dashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("url_path"), req.ID)...)
}

// urlPathValidator rejects a `url_path` without a hyphen at plan time, as HA would at apply
// time.
var urlPathValidator = stringvalidator.RegexMatches(regexp.MustCompile(`-`),
	`must contain a hyphen (-), e.g. "my-home". Home Assistant requires one so that dashboards `+
		`cannot shadow its built-in panels`)
