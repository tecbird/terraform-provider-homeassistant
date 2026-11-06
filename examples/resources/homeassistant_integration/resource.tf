# Each key of `steps` answers the config-flow form with that step_id.
resource "homeassistant_integration" "shelly" {
  domain = "shelly"
  steps = {
    user = { host = "192.168.1.50" }
  }
}

# Secrets go into `sensitive_steps`, which is hidden in plan output. Its fields are merged into
# the step of the same step_id.
resource "homeassistant_integration" "shelly_gen2" {
  domain = "shelly"
  steps = {
    user        = { host = "192.168.1.51" }
    credentials = { username = "admin" }
  }
  sensitive_steps = {
    credentials = { password = var.shelly_password }
  }
}

variable "shelly_password" {
  type      = string
  sensitive = true
}

# A flow that starts with a menu: the menu's step names the chosen option in `next_step_id`, and
# the option's form is a step of its own. This one creates a template sensor helper.
resource "homeassistant_integration" "answer" {
  domain = "template"
  steps = {
    user   = { next_step_id = "sensor" }
    sensor = { name = "Answer", state = "{{ 42 }}" }
  }
}
