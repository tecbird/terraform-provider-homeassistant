# Import a subentry by `<entry_id>/<subentry_id>`. The WebSocket command
# `config_entries/subentries/list` with the entry_id lists the subentry_ids. The steps cannot be read
# back, so they stay null: with an import block, `tofu plan -generate-config-out=subentry.tf` writes
# `steps = null`, which plans no changes. An imported subentry needs steps only to be replaced.
# Write them into the configuration whenever you like, and the next apply records them without
# touching the subentry.
tofu import homeassistant_integration_subentry.kitchen_agent 01JABCDEF0123456789ABCDEFG/01JABCDEF0123456789ABCDEFH
