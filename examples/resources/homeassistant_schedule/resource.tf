# A cheap-tariff window: on weekdays from 22:00 to midnight and from midnight to 06:00, all
# day on weekends. The entity is schedule.cheap_tariff, and it is "on" inside a range.
resource "homeassistant_schedule" "cheap_tariff" {
  name = "Cheap Tariff"
  icon = "mdi:cash-clock"

  monday    = [{ from = "00:00:00", to = "06:00:00" }, { from = "22:00:00", to = "24:00:00" }]
  tuesday   = [{ from = "00:00:00", to = "06:00:00" }, { from = "22:00:00", to = "24:00:00" }]
  wednesday = [{ from = "00:00:00", to = "06:00:00" }, { from = "22:00:00", to = "24:00:00" }]
  thursday  = [{ from = "00:00:00", to = "06:00:00" }, { from = "22:00:00", to = "24:00:00" }]
  friday    = [{ from = "00:00:00", to = "06:00:00" }, { from = "22:00:00", to = "24:00:00" }]
  saturday  = [{ from = "00:00:00", to = "24:00:00" }]
  sunday    = [{ from = "00:00:00", to = "24:00:00" }]
}
