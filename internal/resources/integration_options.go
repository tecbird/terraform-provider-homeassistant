package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

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

var (
	_ resource.ResourceWithConfigure   = (*integrationOptionsResource)(nil)
	_ resource.ResourceWithImportState = (*integrationOptionsResource)(nil)
)

// NewIntegrationOptions returns the `homeassistant_integration_options` resource.
func NewIntegrationOptions() resource.Resource {
	return &integrationOptionsResource{}
}

type integrationOptionsResource struct {
	options client.OptionsFlows
	entries client.ConfigEntries
}

type integrationOptionsModel struct {
	EntryID types.String  `tfsdk:"entry_id"`
	Steps   dyntype.Value `tfsdk:"steps"`
	Domain  types.String  `tfsdk:"domain"`
}

// optionSteps are the fields of `steps` by step_id.
type optionSteps = map[string]map[string]any

func (r *integrationOptionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_integration_options"
}

func (r *integrationOptionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The options of a config entry, set through its options flow: the settings " +
			"behind \"Configure\" on the integration's page, such as a threshold's limits or a " +
			"utility meter's source sensor. The entry can come from the UI or from " +
			"`homeassistant_integration`. Only the fields in `steps` are managed; the flow keeps " +
			"every other option as it is. Home Assistant returns the current options only as the " +
			"pre-filled values of the flow's first form, so each refresh starts the options flow " +
			"and aborts it, which changes nothing; fields of later forms are not compared. " +
			"Destroying the resource leaves the options as they are. Flows that start with a menu " +
			"are not supported.",
		Attributes: map[string]schema.Attribute{
			"entry_id": schema.StringAttribute{
				Description: "The config entry's `entry_id`, e.g. " +
					"`homeassistant_integration.meter.id`. Changing it replaces the resource. " +
					"Import with this ID.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"steps": schema.DynamicAttribute{
				Description: "The options to set, as an object from the options flow's `step_id` " +
					"to the form's fields, e.g. `{ init = { upper = 20 } }`. Each form is answered " +
					"with its current values, and the fields of the step with its `step_id` replace " +
					"them; `null` unsets an optional field. A field the form does not have is an " +
					"error. After an import, this holds every field of the first form.",
				CustomType: dyntype.Type{},
				Required:   true,
				Validators: []validator.Dynamic{stepsValidator{}},
			},
			"domain": computedString("Integration domain of the config entry, e.g. `utility_meter`."),
		},
	}
}

func (r *integrationOptionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c := haClient(req, resp)
	if c == nil {
		return
	}
	r.options = client.NewOptionsFlows(c.REST)
	r.entries = client.NewConfigEntries(c.WS, c.REST)
}

// decodeOptionSteps decodes `steps`, keeping numbers exact; null decodes to nil.
func decodeOptionSteps(v dyntype.Value) (optionSteps, error) {
	if v.IsNull() || v.IsUnderlyingValueNull() {
		return nil, nil
	}
	raw, err := v.JSON()
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var steps optionSteps
	if err := dec.Decode(&steps); err != nil {
		return nil, fmt.Errorf("must be an object from step_id to an object of fields: %w", err)
	}
	return steps, nil
}

// encodeOptionSteps encodes steps as a `steps` value.
func encodeOptionSteps(steps optionSteps) (dyntype.Value, error) {
	raw, err := json.Marshal(steps)
	if err != nil {
		return dyntype.Value{}, err
	}
	return dyntype.FromJSON(raw)
}

// readOptionSteps returns steps with the fields of the form's step replaced by their current
// values. Other steps cannot be read without submitting a form, so they stay as they are. With
// nil steps, which only an import leaves, it returns every field of the form.
func readOptionSteps(steps optionSteps, form client.FlowForm) optionSteps {
	if steps == nil {
		return optionSteps{form.StepID: client.CurrentValues(form.Fields)}
	}
	out := make(optionSteps, len(steps))
	for id, fields := range steps {
		if id == form.StepID {
			fields = client.ReadFields(form.Fields, fields)
		}
		out[id] = fields
	}
	return out
}

// optionsUnchanged reports whether the options already hold steps, as far as the first form
// shows: steps has no other step, and each of its fields equals the current value.
func optionsUnchanged(steps optionSteps, form client.FlowForm) (bool, error) {
	if _, ok := steps[form.StepID]; !ok || len(steps) != 1 {
		return false, nil
	}
	want, err := json.Marshal(steps)
	if err != nil {
		return false, err
	}
	have, err := json.Marshal(readOptionSteps(steps, form))
	if err != nil {
		return false, err
	}
	return dyntype.SemanticallyEqualJSON(want, have)
}

// setOptions runs the options flow with steps, unless the options already hold them. A field the
// first form does not have is an error before anything is submitted.
func (r *integrationOptionsResource) setOptions(ctx context.Context, entryID string, steps optionSteps) error {
	form, err := r.options.Form(ctx, entryID)
	if err != nil {
		return err
	}
	if err := form.CheckFields(steps[form.StepID]); err != nil {
		return err
	}
	unchanged, err := optionsUnchanged(steps, form)
	if err != nil || unchanged {
		return err
	}
	return r.options.Set(ctx, entryID, steps)
}

// entryWithOptions returns the config entry, or ok false if it is gone. An entry whose
// integration has no options flow is an error.
func (r *integrationOptionsResource) entryWithOptions(ctx context.Context, entryID string) (client.ConfigEntry, bool, error) {
	e, ok, err := r.entries.Get(ctx, "", entryID)
	if err != nil || !ok {
		return e, ok, err
	}
	if !e.SupportsOptions {
		return e, true, fmt.Errorf("config entry %s of %s has no options flow", entryID, e.Domain)
	}
	return e, true, nil
}

func (r *integrationOptionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan integrationOptionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entryID := plan.EntryID.ValueString()
	e, ok, err := r.entryWithOptions(ctx, entryID)
	if err == nil && !ok {
		err = fmt.Errorf("config entry %s not found", entryID)
	}
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("entry_id"), "Reading config entry", client.ErrorDetail(err))
		return
	}
	resp.Diagnostics.Append(r.write(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Domain = types.StringValue(e.Domain)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// write sets the options of the plan.
func (r *integrationOptionsResource) write(ctx context.Context, plan integrationOptionsModel) diag.Diagnostics {
	var diags diag.Diagnostics
	steps, err := decodeOptionSteps(plan.Steps)
	if err != nil {
		diags.AddAttributeError(stepsPath, "Invalid steps", err.Error())
		return diags
	}
	if err := r.setOptions(ctx, plan.EntryID.ValueString(), steps); err != nil {
		diags.AddError("Setting the options of the config entry", client.ErrorDetail(err))
	}
	return diags
}

func (r *integrationOptionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state integrationOptionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	entryID := state.EntryID.ValueString()
	e, ok, err := r.entryWithOptions(ctx, entryID)
	if err != nil {
		resp.Diagnostics.AddError("Reading config entry", client.ErrorDetail(err))
		return
	}
	if !ok {
		resp.State.RemoveResource(ctx)
		return
	}
	prior, err := decodeOptionSteps(state.Steps)
	if err != nil {
		resp.Diagnostics.AddAttributeError(stepsPath, "Invalid steps", err.Error())
		return
	}
	form, err := r.options.Form(ctx, entryID)
	if err != nil {
		resp.Diagnostics.AddError("Reading the options of the config entry", client.ErrorDetail(err))
		return
	}
	steps, err := encodeOptionSteps(readOptionSteps(prior, form))
	if err != nil {
		resp.Diagnostics.AddError("Reading the options of the config entry", err.Error())
		return
	}
	state.Steps = steps
	state.Domain = types.StringValue(e.Domain)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *integrationOptionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state integrationOptionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(r.write(ctx, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.Domain = state.Domain
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Delete leaves the options as they are: Home Assistant has no way to restore the values from
// before the resource managed them.
func (r *integrationOptionsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *integrationOptionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("entry_id"), req, resp)
}
