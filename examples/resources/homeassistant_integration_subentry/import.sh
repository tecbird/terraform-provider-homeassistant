# Import a subentry by `<entry_id>/<subentry_id>`. The WebSocket command
# `config_entries/subentries/list` with the entry_id lists the subentry_ids. The steps cannot be read
# back: write them into the configuration afterwards, and the next apply records them without
# touching the subentry.
tofu import homeassistant_integration_subentry.kitchen_agent 01JABCDEF0123456789ABCDEFG/01JABCDEF0123456789ABCDEFH
