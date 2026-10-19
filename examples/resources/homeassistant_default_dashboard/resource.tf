# The built-in "Overview" dashboard. There is only one per Home Assistant instance. If it already
# has a saved config, import it first (see import.sh); destroying the resource resets it to the
# automatically generated dashboard.
resource "homeassistant_default_dashboard" "overview" {
  config = {
    views = [{
      type  = "sections"
      title = "Home"
      sections = [{
        type = "grid"
        cards = [
          { type = "heading", heading = "Quick actions" },
          { type = "button", entity = "script.good_morning" },
        ]
      }]
    }]
  }
}
