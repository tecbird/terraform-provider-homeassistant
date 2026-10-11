# A ten-minute timer, e.g. for a stairwell light. Start it from an automation with the
# timer.start action. The entity is timer.stairwell_light.
resource "homeassistant_timer" "stairwell_light" {
  name     = "Stairwell Light"
  icon     = "mdi:timer-outline"
  duration = "0:10:00"
}
