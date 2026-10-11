# A threshold helper created by Tofu. Its limits live in the options, so changing them later
# runs the options flow instead of replacing the entry.
resource "homeassistant_integration" "frost" {
  domain = "threshold"
  steps = {
    user = { name = "Frost", entity_id = "sensor.outdoor_temperature", lower = 0, hysteresis = 0 }
  }
}

# Each key of `steps` answers the options-flow form with that step_id. Only these fields are
# managed: the flow keeps every other option as it is.
resource "homeassistant_integration_options" "frost" {
  entry_id = homeassistant_integration.frost.id
  steps = {
    init = { lower = 2, hysteresis = 0.5 }
  }
}

# The options of an entry set up in the UI, e.g. a utility meter whose source sensor resets.
resource "homeassistant_integration_options" "water_meter" {
  entry_id = "01JABCDEF0123456789ABCDEFG"
  steps = {
    init = { periodically_resetting = true }
  }
}
