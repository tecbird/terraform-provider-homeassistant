package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func energyPrefs(t *testing.T) client.Energy {
	t.Helper()
	return client.NewEnergy(haClient(t).WS)
}

// checkEnergyPrefs fails unless HA's stored Energy preferences mean the same as want, a JSON
// object with the three keys.
func checkEnergyPrefs(t *testing.T, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		prefs, err := energyPrefs(t).Get(context.Background())
		if err != nil {
			return err
		}
		got, err := prefs.Document()
		if err != nil {
			return err
		}
		eq, err := dyntype.SemanticallyEqualJSON(got, []byte(want))
		if err != nil {
			return err
		}
		if !eq {
			return fmt.Errorf("energy preferences = %s, want %s", got, want)
		}
		return nil
	}
}

// checkEnergyPrefsReset fails unless HA holds empty preferences, which is what a destroy leaves.
func checkEnergyPrefsReset(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		prefs, err := energyPrefs(t).Get(context.Background())
		if err != nil {
			return fmt.Errorf("energy preferences: %w", err)
		}
		doc, err := prefs.Document()
		if err != nil {
			return err
		}
		if string(doc) != `{"device_consumption":[],"device_consumption_water":[],"energy_sources":[]}` {
			return fmt.Errorf("energy preferences left behind: %s", doc)
		}
		return nil
	}
}

// resetEnergyPrefs empties the preferences, e.g. between tests.
func resetEnergyPrefs(t *testing.T) {
	t.Helper()
	if err := energyPrefs(t).Save(context.Background(), client.EnergyPrefs{}); err != nil {
		t.Fatal(err)
	}
}

const accEnergySources = `[
  {"type":"grid","stat_energy_from":"sensor.acc_grid_import","stat_energy_to":"sensor.acc_grid_export",
   "stat_cost":null,"stat_compensation":null,"entity_energy_price":"sensor.acc_price","number_energy_price":null,
   "entity_energy_price_export":null,"number_energy_price_export":0.08,"cost_adjustment_day":0},
  {"type":"solar","stat_energy_from":"sensor.acc_pv_energy","config_entry_solar_forecast":null},
  {"type":"battery","stat_energy_from":"sensor.acc_battery_out","stat_energy_to":"sensor.acc_battery_in"}
]`

const accEnergyHCL = `
resource "homeassistant_energy_preferences" "test" {
  energy_sources = [
    {
      type = "grid"
      stat_energy_from           = "sensor.acc_grid_import"
      stat_energy_to             = "sensor.acc_grid_export"
      stat_cost                  = null
      stat_compensation          = null
      entity_energy_price        = "sensor.acc_price"
      number_energy_price        = null
      entity_energy_price_export = null
      number_energy_price_export = 0.08
      cost_adjustment_day        = 0
    },
    {
      type                        = "solar"
      stat_energy_from            = "sensor.acc_pv_energy"
      config_entry_solar_forecast = null
    },
    {
      type             = "battery"
      stat_energy_from = "sensor.acc_battery_out"
      stat_energy_to   = "sensor.acc_battery_in"
    },
  ]
  device_consumption       = %s
  device_consumption_water = []
}
`

func TestAccEnergyPreferences(t *testing.T) {
	const addr = "homeassistant_energy_preferences.test"
	resetEnergyPrefs(t)
	t.Cleanup(func() { resetEnergyPrefs(t) })
	oven := `[{ stat_consumption = "sensor.acc_oven_energy" }]`
	ovenAndDryer := `[{ stat_consumption = "sensor.acc_oven_energy" }, { stat_consumption = "sensor.acc_dryer_energy" }]`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkEnergyPrefsReset(t),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(accEnergyHCL, oven),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "energy_preferences"),
					checkEnergyPrefs(t, `{"energy_sources":`+accEnergySources+
						`,"device_consumption":[{"stat_consumption":"sensor.acc_oven_energy"}],"device_consumption_water":[]}`),
				),
			},
			{
				// A second plan is empty: what HA stored is not drift.
				Config: acctest.ProviderConfig + fmt.Sprintf(accEnergyHCL, oven),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(accEnergyHCL, ovenAndDryer),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						expectUnchanged{addr, "energy_sources"},
					},
				},
				Check: checkEnergyPrefs(t, `{"energy_sources":`+accEnergySources+
					`,"device_consumption":[{"stat_consumption":"sensor.acc_oven_energy"},{"stat_consumption":"sensor.acc_dryer_energy"}],"device_consumption_water":[]}`),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "energy_preferences",
				ImportStateVerify: true,
			},
			{
				// Edited in the HA UI: shows as drift, and the apply puts it back.
				PreConfig: func() {
					if err := energyPrefs(t).Save(context.Background(), client.EnergyPrefs{
						EnergySources:     json.RawMessage(`[]`),
						DeviceConsumption: json.RawMessage(`[{"stat_consumption":"sensor.acc_other"}]`),
					}); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accEnergyHCL, ovenAndDryer),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkEnergyPrefs(t, `{"energy_sources":`+accEnergySources+
					`,"device_consumption":[{"stat_consumption":"sensor.acc_oven_energy"},{"stat_consumption":"sensor.acc_dryer_energy"}],"device_consumption_water":[]}`),
			},
		},
	})
}

// A create over existing preferences fails, and leaves them as they are.
func TestAccEnergyPreferences_createRefusesExisting(t *testing.T) {
	existing := `[{"stat_consumption":"sensor.acc_keep_me"}]`
	if err := energyPrefs(t).Save(context.Background(), client.EnergyPrefs{DeviceConsumption: json.RawMessage(existing)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resetEnergyPrefs(t) })

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: acctest.ProviderConfig + `
resource "homeassistant_energy_preferences" "test" {
  energy_sources           = []
  device_consumption       = []
  device_consumption_water = []
}
`,
			ExpectError: regexp.MustCompile(`(?s)already has Energy dashboard preferences.*tofu import`),
		}},
	})
	if err := checkEnergyPrefs(t, `{"energy_sources":[],"device_consumption":`+existing+`,"device_consumption_water":[]}`)(nil); err != nil {
		t.Error(err)
	}
}
