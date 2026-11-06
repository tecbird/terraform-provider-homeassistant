# The Energy dashboard: a grid connection with prices, solar, a battery, and tracked devices.
# Start from what the instance has: `tofu plan -generate-config-out=energy.tf` after an import
# block for the ID `energy_preferences`.
resource "homeassistant_energy_preferences" "this" {
  energy_sources = [
    {
      type                       = "grid"
      stat_energy_from           = "sensor.grid_import_energy"
      stat_energy_to             = "sensor.grid_export_energy"
      stat_cost                  = null
      stat_compensation          = null
      entity_energy_price        = null
      number_energy_price        = 0.32
      entity_energy_price_export = null
      number_energy_price_export = 0.08
      cost_adjustment_day        = 0
    },
    {
      type                        = "solar"
      stat_energy_from            = "sensor.pv_energy"
      config_entry_solar_forecast = null
    },
    {
      type             = "battery"
      stat_energy_from = "sensor.battery_energy_out"
      stat_energy_to   = "sensor.battery_energy_in"
    },
  ]

  device_consumption = [
    { stat_consumption = "sensor.heat_pump_energy" },
    { stat_consumption = "sensor.dryer_energy" },
  ]

  device_consumption_water = []
}
