# Counts how often the doorbell rang today, between 0 and 99. The entity is
# counter.doorbell_rings.
resource "homeassistant_counter" "doorbell_rings" {
  name    = "Doorbell Rings"
  icon    = "mdi:bell-ring"
  initial = 0
  minimum = 0
  maximum = 99
  restore = false
}
