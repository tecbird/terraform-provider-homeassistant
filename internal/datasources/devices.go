package datasources

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure      = (*devicesDataSource)(nil)
	_ datasource.DataSourceWithConfigure      = (*deviceDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*deviceDataSource)(nil)
)

// deviceModel is one device, as `homeassistant_device` holds it and as an element of `devices`.
type deviceModel struct {
	DeviceID      types.String `tfsdk:"device_id"`
	Name          types.String `tfsdk:"name"`
	NameByUser    types.String `tfsdk:"name_by_user"`
	Manufacturer  types.String `tfsdk:"manufacturer"`
	Model         types.String `tfsdk:"model"`
	SWVersion     types.String `tfsdk:"sw_version"`
	AreaID        types.String `tfsdk:"area_id"`
	Labels        types.Set    `tfsdk:"labels"`
	ConfigEntries types.List   `tfsdk:"config_entries"`
}

func newDeviceModel(d client.Device) deviceModel {
	entries := make([]attr.Value, len(d.ConfigEntries))
	for i, e := range d.ConfigEntries {
		entries[i] = types.StringValue(e)
	}
	return deviceModel{
		DeviceID:      types.StringValue(d.ID),
		Name:          types.StringPointerValue(d.Name),
		NameByUser:    types.StringPointerValue(d.NameByUser),
		Manufacturer:  types.StringPointerValue(d.Manufacturer),
		Model:         types.StringPointerValue(d.Model),
		SWVersion:     types.StringPointerValue(d.SWVersion.StringPointer()),
		AreaID:        types.StringPointerValue(d.AreaID),
		Labels:        stringSet(d.Labels),
		ConfigEntries: types.ListValueMust(types.StringType, entries),
	}
}

// deviceFields describes the attributes of a device other than device_id and name.
var deviceFields = map[string]string{
	"name_by_user":   "Name the user gave the device; null if it has none.",
	"manufacturer":   "Manufacturer; null if the integration reports none.",
	"model":          "Model; null if the integration reports none.",
	"sw_version":     "Firmware or software version; null if the integration reports none.",
	"area_id":        "ID of the device's area; null if it is in none.",
	"labels":         "IDs of the device's labels.",
	"config_entries": "IDs of the config entries the device belongs to.",
}

// computedDeviceAttributes returns the schema of deviceFields, all computed.
func computedDeviceAttributes() map[string]schema.Attribute {
	attrs := make(map[string]schema.Attribute, len(deviceFields))
	for name, desc := range deviceFields {
		switch name {
		case "labels":
			attrs[name] = schema.SetAttribute{Description: desc, ElementType: types.StringType, Computed: true}
		case "config_entries":
			attrs[name] = schema.ListAttribute{Description: desc, ElementType: types.StringType, Computed: true}
		default:
			attrs[name] = schema.StringAttribute{Description: desc, Computed: true}
		}
	}
	return attrs
}

// deviceRegistryClient returns the device registry from the provider data, or nil before the
// provider is configured or on error.
func deviceRegistryClient(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *client.Registry[client.Device] {
	c := haClient(req, resp)
	if c == nil {
		return nil
	}
	r := client.Devices(c.WS)
	return &r
}

// NewDevices returns the `homeassistant_devices` data source.
func NewDevices() datasource.DataSource {
	return &devicesDataSource{}
}

type devicesDataSource struct {
	devices *client.Registry[client.Device]
	entries client.ConfigEntries
}

type devicesModel struct {
	Integration  types.String  `tfsdk:"integration"`
	Manufacturer types.String  `tfsdk:"manufacturer"`
	Model        types.String  `tfsdk:"model"`
	AreaID       types.String  `tfsdk:"area_id"`
	Label        types.String  `tfsdk:"label"`
	Devices      []deviceModel `tfsdk:"devices"`
}

func (d *devicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_devices"
}

func (d *devicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := computedDeviceAttributes()
	attrs["device_id"] = schema.StringAttribute{Description: "Device ID.", Computed: true}
	attrs["name"] = schema.StringAttribute{
		Description: "Name the integration gave the device; null if it gave none.",
		Computed:    true,
	}
	resp.Schema = schema.Schema{
		Description: "Lists the devices that match every filter that is set " +
			"(`config/device_registry/list`). Without filters, it lists every device. Filters " +
			"compare exactly.",
		Attributes: map[string]schema.Attribute{
			"integration": schema.StringAttribute{
				Description: "Only devices of a config entry of this integration domain, e.g. `hue`. " +
					"An unknown domain matches no devices.",
				Optional: true,
			},
			"manufacturer": schema.StringAttribute{
				Description: "Only devices of this manufacturer.",
				Optional:    true,
			},
			"model": schema.StringAttribute{
				Description: "Only devices of this model.",
				Optional:    true,
			},
			"area_id": schema.StringAttribute{
				Description: "Only devices in the area with this ID.",
				Optional:    true,
			},
			"label": schema.StringAttribute{
				Description: "Only devices with the label with this ID.",
				Optional:    true,
			},
			"devices": schema.ListNestedAttribute{
				Description:  "The matching devices, sorted by `device_id`.",
				Computed:     true,
				NestedObject: schema.NestedAttributeObject{Attributes: attrs},
			},
		},
	}
}

func (d *devicesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if c := haClient(req, resp); c != nil {
		r := client.Devices(c.WS)
		d.devices = &r
		d.entries = client.NewConfigEntries(c.WS, c.REST)
	}
}

func (d *devicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg devicesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := deviceFilter{
		manufacturer: cfg.Manufacturer.ValueStringPointer(),
		model:        cfg.Model.ValueStringPointer(),
		areaID:       cfg.AreaID.ValueStringPointer(),
		label:        cfg.Label.ValueStringPointer(),
	}
	if !cfg.Integration.IsNull() {
		domain := cfg.Integration.ValueString()
		entries, err := d.entries.List(ctx, domain)
		if err != nil {
			resp.Diagnostics.AddError(fmt.Sprintf("Reading the config entries of %q", domain), client.ErrorDetail(err))
			return
		}
		f.entries = map[string]bool{}
		for _, e := range entries {
			f.entries[e.EntryID] = true
		}
	}

	devices, err := d.devices.List(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading devices", client.ErrorDetail(err))
		return
	}
	matched := f.apply(devices)
	cfg.Devices = make([]deviceModel, len(matched))
	for i, dev := range matched {
		cfg.Devices[i] = newDeviceModel(dev)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}

// deviceFilter holds the filters of the devices data source. Nil fields don't filter.
type deviceFilter struct {
	manufacturer, model, areaID, label *string
	// entries are the IDs of the config entries of the integration.
	entries map[string]bool
}

// apply returns the devices that pass every filter, sorted by ID.
func (f deviceFilter) apply(devices []client.Device) []client.Device {
	var out []client.Device
	for _, d := range devices {
		if f.matches(d) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f deviceFilter) matches(d client.Device) bool {
	for _, c := range []struct{ want, got *string }{
		{f.manufacturer, d.Manufacturer}, {f.model, d.Model}, {f.areaID, d.AreaID},
	} {
		if c.want != nil && (c.got == nil || *c.got != *c.want) {
			return false
		}
	}
	if f.label != nil && !slices.Contains(d.Labels, *f.label) {
		return false
	}
	if f.entries != nil && !slices.ContainsFunc(d.ConfigEntries, func(e string) bool { return f.entries[e] }) {
		return false
	}
	return true
}

// NewDevice returns the `homeassistant_device` data source.
func NewDevice() datasource.DataSource {
	return &deviceDataSource{}
}

type deviceDataSource struct {
	devices *client.Registry[client.Device]
}

func (d *deviceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_device"
}

func (d *deviceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := computedDeviceAttributes()
	attrs["device_id"] = schema.StringAttribute{
		Description: "Device ID to look up. Set exactly one of `device_id` and `name`.",
		Optional:    true,
		Computed:    true,
	}
	attrs["name"] = schema.StringAttribute{
		Description: "Name to look up. It is compared with `name_by_user` first; only if no device " +
			"has that `name_by_user` is it compared with the name the integration gave. Set exactly " +
			"one of `device_id` and `name`. When the device is looked up by `device_id`, this is the " +
			"name the integration gave, or null if it gave none.",
		Optional: true,
		Computed: true,
	}
	resp.Schema = schema.Schema{
		Description: "Reads one device (`config/device_registry/list`), by ID or by name. Exactly " +
			"one device must match.",
		Attributes: attrs,
	}
}

func (d *deviceDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	resp.Diagnostics.Append(exactlyOneOf(ctx, req.Config, "device_id", "name")...)
}

func (d *deviceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if r := deviceRegistryClient(req, resp); r != nil {
		d.devices = r
	}
}

func (d *deviceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg deviceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	devices, err := d.devices.List(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading devices", client.ErrorDetail(err))
		return
	}
	device, err := matchDevice(devices, cfg.DeviceID.ValueStringPointer(), cfg.Name.ValueStringPointer())
	if err != nil {
		resp.Diagnostics.AddError("Finding the device", err.Error())
		return
	}
	state := newDeviceModel(device)
	if !cfg.Name.IsNull() {
		// The configured value must stay, even if it matched name_by_user.
		state.Name = cfg.Name
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// matchDevice returns the one device with the given ID or, if id is nil, the given name: the
// devices whose name_by_user is name, or else those whose name is.
func matchDevice(devices []client.Device, id, name *string) (client.Device, error) {
	var matches []client.Device
	var what string
	if id != nil {
		what = fmt.Sprintf("ID %q", *id)
		for _, d := range devices {
			if d.ID == *id {
				matches = append(matches, d)
			}
		}
	} else {
		what = fmt.Sprintf("name %q", *name)
		for _, field := range []func(client.Device) *string{
			func(d client.Device) *string { return d.NameByUser },
			func(d client.Device) *string { return d.Name },
		} {
			for _, d := range devices {
				if n := field(d); n != nil && *n == *name {
					matches = append(matches, d)
				}
			}
			if len(matches) > 0 {
				break
			}
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return client.Device{}, fmt.Errorf("no device has %s", what)
	}
	ids := make([]string, len(matches))
	for i, d := range matches {
		ids[i] = d.ID
	}
	sort.Strings(ids)
	return client.Device{}, fmt.Errorf("%d devices have %s: %s; look the device up by `device_id`",
		len(matches), what, strings.Join(ids, ", "))
}
