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

# A flow that shows the same step_id more than once (here two forms, both "user") is answered by a
# list for that step_id: the entries answer its forms in order. Give `sensitive_steps` a list of
# the same length, with `{}` for a form without secrets.
resource "homeassistant_integration" "llama" {
  domain = "llama_conversation"
  steps = {
    user = [
      { backend_type = "Ollama API" },
      { host = "192.168.1.60", port = 11434, ssl = false },
    ]
  }
  sensitive_steps = {
    user = [{}, { api_key = var.ollama_api_key }]
  }
}

variable "ollama_api_key" {
  type      = string
  sensitive = true
}
