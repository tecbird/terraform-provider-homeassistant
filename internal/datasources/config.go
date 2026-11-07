package datasources

import (
	"context"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

var _ datasource.DataSourceWithConfigure = (*configDataSource)(nil)

// configReader is the part of the client the config data source uses.
type configReader interface {
	Config(ctx context.Context) (*client.InstanceConfig, error)
}

// NewConfig returns the `homeassistant_config` data source.
func NewConfig() datasource.DataSource {
	return &configDataSource{}
}

type configDataSource struct {
	client configReader
}

type configModel struct {
	Version      types.String  `tfsdk:"version"`
	LocationName types.String  `tfsdk:"location_name"`
	Latitude     types.Float64 `tfsdk:"latitude"`
	Longitude    types.Float64 `tfsdk:"longitude"`
	Elevation    types.Float64 `tfsdk:"elevation"`
	TimeZone     types.String  `tfsdk:"time_zone"`
	UnitSystem   types.Object  `tfsdk:"unit_system"`
	Components   types.List    `tfsdk:"components"`
}

var unitSystemAttrTypes = map[string]attr.Type{
	"temperature": types.StringType,
	"length":      types.StringType,
	"mass":        types.StringType,
	"volume":      types.StringType,
}

func (d *configDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_config"
}

func (d *configDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the configuration of the Home Assistant instance (`GET /api/config`).",
		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				Description: "Home Assistant version, e.g. `2026.11.0`.",
				Computed:    true,
			},
			"location_name": schema.StringAttribute{
				Description: "Name of the location.",
				Computed:    true,
			},
			"latitude": schema.Float64Attribute{
				Description: "Latitude of the location.",
				Computed:    true,
			},
			"longitude": schema.Float64Attribute{
				Description: "Longitude of the location.",
				Computed:    true,
			},
			"elevation": schema.Float64Attribute{
				Description: "Elevation of the location, in meters.",
				Computed:    true,
			},
			"time_zone": schema.StringAttribute{
				Description: "IANA time zone, e.g. `Europe/Berlin`.",
				Computed:    true,
			},
			"unit_system": schema.SingleNestedAttribute{
				Description: "Units the instance displays values in.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"temperature": schema.StringAttribute{Description: "Temperature unit, e.g. `°C`.", Computed: true},
					"length":      schema.StringAttribute{Description: "Length unit, e.g. `km`.", Computed: true},
					"mass":        schema.StringAttribute{Description: "Mass unit, e.g. `g`.", Computed: true},
					"volume":      schema.StringAttribute{Description: "Volume unit, e.g. `L`.", Computed: true},
				},
			},
			"components": schema.ListAttribute{
				Description: "Loaded components (integrations and platforms), sorted.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *configDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if c := restClient(req, resp); c != nil {
		d.client = c
	}
}

func (d *configDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	cfg, err := d.client.Config(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Reading Home Assistant config", client.ErrorDetail(err))
		return
	}

	unitSystem, diags := types.ObjectValue(unitSystemAttrTypes, map[string]attr.Value{
		"temperature": types.StringValue(cfg.UnitSystem.Temperature),
		"length":      types.StringValue(cfg.UnitSystem.Length),
		"mass":        types.StringValue(cfg.UnitSystem.Mass),
		"volume":      types.StringValue(cfg.UnitSystem.Volume),
	})
	resp.Diagnostics.Append(diags...)
	// HA builds the list from a set, so its order changes between restarts.
	components := slices.Sorted(slices.Values(cfg.Components))
	componentList, diags := types.ListValueFrom(ctx, types.StringType, components)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, configModel{
		Version:      types.StringValue(cfg.Version),
		LocationName: types.StringValue(cfg.LocationName),
		Latitude:     types.Float64Value(cfg.Latitude),
		Longitude:    types.Float64Value(cfg.Longitude),
		Elevation:    types.Float64Value(cfg.Elevation),
		TimeZone:     types.StringValue(cfg.TimeZone),
		UnitSystem:   unitSystem,
		Components:   componentList,
	})...)
}
