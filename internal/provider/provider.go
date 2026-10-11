// Package provider defines the provider schema, Configure, and the registration of resources,
// data sources, and functions.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/datasources"
	"github.com/toelke/terraform-provider-homeassistant/internal/functions"
	"github.com/toelke/terraform-provider-homeassistant/internal/resources"
)

var _ provider.ProviderWithFunctions = (*HomeAssistantProvider)(nil)

// HomeAssistantProvider is the `homeassistant` provider.
type HomeAssistantProvider struct {
	version string
}

// providerModel maps the provider block.
type providerModel struct {
	URL      types.String `tfsdk:"url"`
	Token    types.String `tfsdk:"token"`
	Insecure types.Bool   `tfsdk:"insecure"`
	Timeout  types.String `tfsdk:"timeout"`
}

// New returns a constructor for the provider, as providerserver.Serve expects.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HomeAssistantProvider{version: version}
	}
}

func (p *HomeAssistantProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "homeassistant"
	resp.Version = p.version
}

func (p *HomeAssistantProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Home Assistant instance as code.",
		Attributes: map[string]schema.Attribute{
			"url": schema.StringAttribute{
				Description: "Base URL of the Home Assistant instance, without `/api`, e.g. " +
					"`http://homeassistant.local:8123`. Required; can also be set with `" + envURL + "`.",
				Optional: true,
			},
			"token": schema.StringAttribute{
				Description: "Long-lived access token of an admin user. Required; can also be set with `" +
					envToken + "`.",
				Optional:  true,
				Sensitive: true,
			},
			"insecure": schema.BoolAttribute{
				Description: "Skip TLS certificate verification. Defaults to `false`; can also be set with `" +
					envInsecure + "`.",
				Optional: true,
			},
			"timeout": schema.StringAttribute{
				Description: "Per-request timeout as a Go duration, e.g. `\"30s\"`. Defaults to `\"30s\"`; " +
					"can also be set with `" + envTimeout + "`.",
				Optional: true,
			},
		},
	}
}

func (p *HomeAssistantProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var model providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg, diags := resolveConfig(model, os.LookupEnv)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	c := client.New(cfg)
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *HomeAssistantProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resources.NewAutomation,
		resources.NewArea,
		resources.NewDashboard,
		resources.NewDefaultDashboard,
		resources.NewDeviceSettings,
		resources.NewEntitySettings,
		resources.NewESPHome,
		resources.NewFloor,
		resources.NewInputBoolean,
		resources.NewInputButton,
		resources.NewInputDatetime,
		resources.NewInputNumber,
		resources.NewInputSelect,
		resources.NewInputText,
		resources.NewIntegration,
		resources.NewLabel,
		resources.NewMQTT,
		resources.NewScene,
		resources.NewScript,
	}
}

func (p *HomeAssistantProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasources.NewConfig,
		datasources.NewEntity,
		datasources.NewEntities,
		datasources.NewIntegration,
		datasources.NewTemplate,
		datasources.NewArea,
		datasources.NewAreas,
		datasources.NewDevice,
		datasources.NewDevices,
	}
}

func (p *HomeAssistantProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{
		functions.NewAction,
		functions.NewDelay,
		functions.NewEntitiesCard,
		functions.NewGridSection,
		functions.NewHeadingCard,
		functions.NewNumericStateTrigger,
		functions.NewStateCondition,
		functions.NewStateTrigger,
		functions.NewTileCard,
		functions.NewTimeCondition,
		functions.NewTimeTrigger,
	}
}
