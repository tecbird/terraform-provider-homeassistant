package resources

import (
	"context"
	"encoding/json"
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
	_ resource.ResourceWithConfigure   = (*energyPreferencesResource)(nil)
	_ resource.ResourceWithImportState = (*energyPreferencesResource)(nil)
)

// energyPreferencesID is the `id` of the one `homeassistant_energy_preferences` an instance can
// have, and its import ID.
const energyPreferencesID = "energy_preferences"

// NewEnergyPreferences returns the `homeassistant_energy_preferences` resource.
func NewEnergyPreferences() resource.Resource {
	return &energyPreferencesResource{}
}

type energyPreferencesResource struct {
	energy client.Energy
}

type energyPreferencesModel struct {
	ID                     types.String  `tfsdk:"id"`
	EnergySources          dyntype.Value `tfsdk:"energy_sources"`
	DeviceConsumption      dyntype.Value `tfsdk:"device_consumption"`
	DeviceConsumptionWater dyntype.Value `tfsdk:"device_consumption_water"`
}

// prefs returns the model as the client's preferences.
func (m energyPreferencesModel) prefs() (client.EnergyPrefs, error) {
	var p client.EnergyPrefs
	for _, f := range []struct {
		name string
		from dyntype.Value
		to   *json.RawMessage
	}{
		{"energy_sources", m.EnergySources, &p.EnergySources},
		{"device_consumption", m.DeviceConsumption, &p.DeviceConsumption},
		{"device_consumption_water", m.DeviceConsumptionWater, &p.DeviceConsumptionWater},
	} {
		raw, err := f.from.JSON()
		if err != nil {
			return p, fmt.Errorf("%s: %w", f.name, err)
		}
		*f.to = raw
	}
	return p, nil
}

// energyPreferencesFrom returns the model for prefs as read from HA. A key HA does not return
// is an empty list.
func energyPreferencesFrom(prefs client.EnergyPrefs) (energyPreferencesModel, error) {
	doc, err := prefs.Document()
	if err != nil {
		return energyPreferencesModel{}, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(doc, &keys); err != nil {
		return energyPreferencesModel{}, err
	}
	m := energyPreferencesModel{ID: types.StringValue(energyPreferencesID)}
	for key, to := range map[string]*dyntype.Value{
		"energy_sources":           &m.EnergySources,
		"device_consumption":       &m.DeviceConsumption,
		"device_consumption_water": &m.DeviceConsumptionWater,
	} {
		if *to, err = dyntype.FromJSON(keys[key]); err != nil {
			return energyPreferencesModel{}, fmt.Errorf("%s: %w", key, err)
		}
	}
	return m, nil
}

func (r *energyPreferencesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_energy_preferences"
}

func (r *energyPreferencesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	listAttribute := func(description string) schema.DynamicAttribute {
		return schema.DynamicAttribute{
			Description: description + " Pass `[]` for none. Home Assistant validates the contents " +
				"when it saves them, not when you plan.",
			CustomType: dyntype.Type{},
			Required:   true,
		}
	}
	resp.Schema = schema.Schema{
		Description: "The Energy dashboard preferences of the instance: grid, solar, battery and " +
			"gas sources, and the individual devices. There is exactly one per instance, so a " +
			"second resource of this type fights the first. Destroying it resets the preferences " +
			"to empty lists.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Always `" + energyPreferencesID + "`. Import with this ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"energy_sources": listAttribute("The energy sources, as a list of objects with a `type` " +
				"(`grid`, `solar`, `battery`, `gas`, `water`) and the fields of that type, exactly as " +
				"`energy/get_prefs` returns them."),
			"device_consumption": listAttribute("The individual devices whose energy use is tracked, " +
				"as a list of objects with a `stat_consumption`."),
			"device_consumption_water": listAttribute("The individual devices whose water use is " +
				"tracked, as a list of objects with a `stat_consumption`."),
		},
	}
}

func (r *energyPreferencesResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.energy = client.NewEnergy(c.WS)
}

func (r *energyPreferencesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan energyPreferencesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The preferences always exist, so a create would overwrite someone's Energy dashboard
	// without a word. Like a config item whose ID is taken (ADR-0024), it asks for an import.
	existing, err := r.energy.Get(ctx)
	if err != nil && !errors.Is(err, client.ErrNoEnergyPrefs) {
		resp.Diagnostics.AddError("Reading the energy preferences", client.ErrorDetail(err))
		return
	}
	if !energyPrefsEmpty(existing) {
		resp.Diagnostics.AddError("Energy preferences already set",
			"Home Assistant already has Energy dashboard preferences, and creating this resource would "+
				"overwrite them. Import them with `tofu import homeassistant_energy_preferences.<name> "+
				energyPreferencesID+"`, or empty them in Home Assistant first.")
		return
	}

	r.save(ctx, plan, resp.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ID = types.StringValue(energyPreferencesID)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *energyPreferencesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state energyPreferencesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	read, ok := r.read(ctx, state, req.Private, &resp.Diagnostics)
	switch {
	case resp.Diagnostics.HasError():
	case !ok:
		resp.State.RemoveResource(ctx)
	default:
		resp.Diagnostics.Append(resp.State.Set(ctx, read)...)
	}
}

// read returns the model for state after a refresh. ok is false if HA has no preferences stored.
func (r *energyPreferencesResource) read(ctx context.Context, state energyPreferencesModel, private privateGetter, diags *diag.Diagnostics) (_ energyPreferencesModel, ok bool) {
	prefs, err := r.energy.Get(ctx)
	if errors.Is(err, client.ErrNoEnergyPrefs) {
		return state, false
	}
	if err != nil {
		diags.AddError("Reading the energy preferences", client.ErrorDetail(err))
		return state, false
	}
	read, err := energyPreferencesFrom(prefs)
	if err != nil {
		diags.AddError("Reading the energy preferences", err.Error())
		return state, false
	}

	// What HA returns equals the baseline: nothing changed outside Tofu (ADR-0023), so state keeps
	// the values as the user wrote them. Otherwise the semantic equality of ADR-0006 decides, per
	// attribute.
	doc, err := prefs.Document()
	if err != nil {
		diags.AddError("Reading the energy preferences", err.Error())
		return state, false
	}
	baseline, d := storedBaseline(ctx, private)
	diags.Append(d...)
	if d.HasError() {
		return state, false
	}
	match, err := matchesBaseline(doc, baseline)
	if err != nil {
		diags.AddError("Reading the energy preferences", err.Error())
		return state, false
	}
	if match {
		read = state
	}
	read.ID = types.StringValue(energyPreferencesID)
	return read, true
}

func (r *energyPreferencesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan energyPreferencesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.save(ctx, plan, resp.Private, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// save writes the preferences, reads them back and stores them as the baseline (ADR-0023).
// Failing to read them back is only a warning: without a baseline, Read falls back to semantic
// equality.
func (r *energyPreferencesResource) save(ctx context.Context, plan energyPreferencesModel, private privateSetter, diags *diag.Diagnostics) {
	prefs, err := plan.prefs()
	if err != nil {
		diags.AddError("Invalid energy preferences", err.Error())
		return
	}
	if err := r.energy.Save(ctx, prefs); err != nil {
		diags.AddError("Saving the energy preferences", client.ErrorDetail(err))
		return
	}
	stored, err := r.energy.Get(ctx)
	if err == nil {
		var doc json.RawMessage
		if doc, err = stored.Document(); err == nil {
			diags.Append(storeBaseline(ctx, private, doc)...)
			return
		}
	}
	diags.AddWarning("Reading the energy preferences back",
		client.ErrorDetail(err)+"\n\nThe preferences were saved. Until the next apply, changes that "+
			"Home Assistant made on save may show as a difference.")
}

// Delete resets the preferences to empty lists: HA has no way to delete them.
func (r *energyPreferencesResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	if err := r.energy.Save(ctx, client.EnergyPrefs{}); err != nil {
		resp.Diagnostics.AddError("Resetting the energy preferences", client.ErrorDetail(err))
	}
}

func (r *energyPreferencesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != energyPreferencesID {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("The import ID of the energy preferences is %q, got %q.", energyPreferencesID, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}

// energyPrefsEmpty reports whether prefs hold no source and no device.
func energyPrefsEmpty(prefs client.EnergyPrefs) bool {
	for _, raw := range []json.RawMessage{prefs.EnergySources, prefs.DeviceConsumption, prefs.DeviceConsumptionWater} {
		if s := string(raw); s != "" && s != "null" && s != "[]" {
			return false
		}
	}
	return true
}
