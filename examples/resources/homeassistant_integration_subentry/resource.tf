resource "homeassistant_integration" "anthropic" {
  domain          = "anthropic"
  steps           = { user = {} }
  sensitive_steps = { user = { api_key = var.anthropic_api_key } }
}

# A conversation agent of the integration. Each key of `steps` answers the subentry-flow form with
# that step_id; `sensitive_steps` works as in `homeassistant_integration`. Conversation agents have
# a reconfigure flow, so changing the prompt later changes the agent in place.
resource "homeassistant_integration_subentry" "kitchen_agent" {
  entry_id      = homeassistant_integration.anthropic.id
  subentry_type = "conversation"
  steps = {
    init = {
      name         = "Kitchen assistant"
      prompt       = "You help with cooking and the kitchen appliances."
      llm_hass_api = ["assist"]
      recommended  = true
    }
  }
}

variable "anthropic_api_key" {
  type      = string
  sensitive = true
}
