package resources

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*entitySettingsResource)(nil)
	_ resource.ResourceWithImportState = (*entitySettingsResource)(nil)
)

// defaultSettingsCreateTimeout is how long create waits for the registry entry to appear
// (ADR-0010).
const defaultSettingsCreateTimeout = 60 * time.Second

// NewEntitySettings returns the `homeassistant_entity_settings` resource.
func NewEntitySettings() resource.Resource {
	return &entitySettingsResource{}
}

type entitySettingsResource struct {
	entities client.EntityRegistry
}

type entitySettingsModel struct {
	EntityID     types.String   `tfsdk:"entity_id"`
	Name         types.String   `tfsdk:"name"`
	Icon         types.String   `tfsdk:"icon"`
	AreaID       types.String   `tfsdk:"area_id"`
	Labels       types.Set      `tfsdk:"labels"`
	Hidden       types.Bool     `tfsdk:"hidden"`
	Disabled     types.Bool     `tfsdk:"disabled"`
	Platform     types.String   `tfsdk:"platform"`
	DeviceID     types.String   `tfsdk:"device_id"`
	OriginalName types.String   `tfsdk:"original_name"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

func (r *entitySettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_entity_settings"
}

func (r *entitySettingsResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Settings of an entity that an integration owns, as in the entity settings " +
			"dialog: name, icon, area, labels, hidden, disabled. Only the attributes you set are " +
			"managed; changes made elsewhere to the others cause no diff. Destroying the resource " +
			"resets the attributes you set to Home Assistant's defaults and leaves the entity in " +
			"place. Values from before Tofu managed the entity are not restored. Only entities with " +
			"a unique ID are in the entity registry and can be managed. Import with the entity ID; " +
			"the first plan after an import sets the configured attributes.",
		Attributes: map[string]schema.Attribute{
			"entity_id": schema.StringAttribute{
				Description: "Entity ID, e.g. `light.bedroom_ceiling`. Changing it replaces the " +
					"resource. Create waits for the entity to appear in the registry.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description: "Name that overrides the integration's. Unsetting it goes back to " +
					"the integration's name. Since Home Assistant 2026.11, Home Assistant shows the " +
					"device name in front of it, as it does for the integration's name, unless the " +
					"entity has an area of its own. Leave the device name out then (`Power`, not " +
					"`Kitchen Lamp Power`).",
				Optional: true,
			},
			"icon": schema.StringAttribute{
				Description: "Icon that overrides the integration's, e.g. `mdi:ceiling-light`.",
				Optional:    true,
			},
			"area_id": schema.StringAttribute{
				Description: "ID of the entity's area, e.g. `homeassistant_area.bedroom.id`. " +
					"Unsetting it makes the entity inherit its device's area.",
				Optional: true,
			},
			"labels": schema.SetAttribute{
				Description: "IDs of the labels on the entity. When set, this is the complete " +
					"list: labels added elsewhere are removed.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"hidden": schema.BoolAttribute{
				Description: "Whether the user hides the entity. Reads `false` when it is " +
					"visible or hidden by its integration. `false` unhides an entity the user " +
					"hid, but leaves one hidden by its integration hidden.",
				Optional: true,
			},
			"disabled": schema.BoolAttribute{
				Description: "Whether the user disables the entity. Reads `false` when it is " +
					"enabled or disabled by its integration or device. `false` enables an entity " +
					"the user disabled, but leaves one disabled by its integration or device disabled.",
				Optional: true,
			},
			"platform":      computedString("Integration that owns the entity, e.g. `hue`."),
			"device_id":     computedString("ID of the entity's device, if it has one."),
			"original_name": computedString("Name the integration gives the entity."),
			"timeouts":      timeouts.Attributes(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *entitySettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.entities = client.Entities(c.WS)
}

// changes returns the registry fields that turn from into to: those set in to that differ from
// from, and those set only in from, reset to their defaults. Unset attributes are left out, so
// only configured fields are ever written (ADR-0010). Hidden and disabled follow flagChange, so
// from must hold their registry values where to sets them (withRegistryFlags).
func (to entitySettingsModel) changes(ctx context.Context, from entitySettingsModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	fields := map[string]any{}
	fieldChange(fields, "name", to.Name, from.Name, func() any { return to.Name.ValueStringPointer() }, nil)
	fieldChange(fields, "icon", to.Icon, from.Icon, func() any { return to.Icon.ValueStringPointer() }, nil)
	fieldChange(fields, "area_id", to.AreaID, from.AreaID, func() any { return to.AreaID.ValueStringPointer() }, nil)
	fieldChange(fields, "labels", to.Labels, from.Labels, func() any {
		labels := []string{}
		diags.Append(to.Labels.ElementsAs(ctx, &labels, false)...)
		return labels
	}, []string{})
	flagChange(fields, "hidden_by", to.Hidden, from.Hidden)
	flagChange(fields, "disabled_by", to.Disabled, from.Disabled)
	return fields, diags
}

// needsRegistryFlags reports whether withRegistryFlags needs the entry to turn from into to.
func (to entitySettingsModel) needsRegistryFlags(from entitySettingsModel) bool {
	return needsRegistryFlag(to.Hidden, from.Hidden) || needsRegistryFlag(to.Disabled, from.Disabled)
}

// withRegistryFlags returns from with the flags that to sets and from does not taken from e.
func (to entitySettingsModel) withRegistryFlags(from entitySettingsModel, e client.EntityEntry) entitySettingsModel {
	from.Hidden = registryFlag(to.Hidden, from.Hidden, e.HiddenBy)
	from.Disabled = registryFlag(to.Disabled, from.Disabled, e.DisabledBy)
	return from
}

// refresh reads e into m: the computed attributes, and the settable ones that m manages.
func (m *entitySettingsModel) refresh(ctx context.Context, e client.EntityEntry) diag.Diagnostics {
	var diags diag.Diagnostics
	m.EntityID = types.StringValue(e.EntityID)
	m.computedFrom(e)
	if !m.Name.IsNull() {
		m.Name = types.StringPointerValue(e.Name)
	}
	if !m.Icon.IsNull() {
		m.Icon = types.StringPointerValue(e.Icon)
	}
	if !m.AreaID.IsNull() {
		m.AreaID = types.StringPointerValue(e.AreaID)
	}
	if !m.Labels.IsNull() {
		m.Labels, diags = types.SetValueFrom(ctx, types.StringType, append([]string{}, e.Labels...))
	}
	if !m.Hidden.IsNull() {
		m.Hidden = types.BoolValue(userFlag(e.HiddenBy))
	}
	if !m.Disabled.IsNull() {
		m.Disabled = types.BoolValue(userFlag(e.DisabledBy))
	}
	return diags
}

// computedFrom sets m's computed attributes from e and leaves the settable ones alone.
func (m *entitySettingsModel) computedFrom(e client.EntityEntry) {
	m.Platform = types.StringValue(e.Platform)
	m.DeviceID = types.StringPointerValue(e.DeviceID)
	m.OriginalName = types.StringPointerValue(e.OriginalName)
}

// awaitEntity polls the registry until entityID is in it, for at most timeout.
func (r *entitySettingsResource) awaitEntity(ctx context.Context, entityID string, timeout time.Duration) (client.EntityEntry, error) {
	return awaitRegistry(ctx, timeout, func(ctx context.Context) (client.EntityEntry, bool, error) {
		return r.entities.Get(ctx, entityID)
	}, fmt.Errorf("entity %s did not appear in the entity registry within %s.\n\n"+
		"Check that the entity ID is correct and that the integration providing it is set up. "+
		"Entities without a unique ID are never in the registry and cannot be managed. If the "+
		"integration needs longer, raise `timeouts.create`", entityID, timeout))
}

func (r *entitySettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan entitySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, diags := plan.Timeouts.Create(ctx, defaultSettingsCreateTimeout)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	entityID := plan.EntityID.ValueString()
	entry, err := r.awaitEntity(ctx, entityID, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Waiting for entity", client.ErrorDetail(err))
		return
	}
	fields, diags := plan.changes(ctx, plan.withRegistryFlags(entitySettingsModel{}, entry))
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if len(fields) > 0 {
		if entry, err = r.entities.Update(ctx, entityID, fields); err != nil {
			resp.Diagnostics.AddError("Updating entity settings", client.ErrorDetail(err))
			return
		}
	}

	plan.computedFrom(entry)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *entitySettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state entitySettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	entry, ok, err := r.entities.Get(ctx, state.EntityID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading entity settings", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(state.refresh(ctx, entry)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *entitySettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state entitySettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entityID := plan.EntityID.ValueString()
	from := state
	if plan.needsRegistryFlags(state) {
		entry, ok, err := r.entities.Get(ctx, entityID)
		if err == nil && !ok {
			err = fmt.Errorf("entity %s is not in the entity registry", entityID)
		}
		if err != nil {
			resp.Diagnostics.AddError("Reading entity settings", client.ErrorDetail(err))
			return
		}
		from = plan.withRegistryFlags(state, entry)
	}
	fields, diags := plan.changes(ctx, from)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if len(fields) > 0 {
		entry, err := r.entities.Update(ctx, entityID, fields)
		if err != nil {
			resp.Diagnostics.AddError("Updating entity settings", client.ErrorDetail(err))
			return
		}
		plan.computedFrom(entry)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *entitySettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state entitySettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	fields, diags := entitySettingsModel{}.changes(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || len(fields) == 0 {
		return
	}

	entityID := state.EntityID.ValueString()
	_, err := r.entities.Update(ctx, entityID, fields)
	// An entity that is gone has nothing left to reset.
	err = client.IgnoreGone(ctx, err, func(ctx context.Context) (bool, error) {
		_, ok, err := r.entities.Get(ctx, entityID)
		return ok, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Resetting entity settings", client.ErrorDetail(err))
	}
}

func (r *entitySettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("entity_id"), req, resp)
}
