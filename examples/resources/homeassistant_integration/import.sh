# Import a config entry by its entry_id. The steps cannot be read back, so they stay null: with an
# import block, `tofu plan -generate-config-out=integration.tf` writes `steps = null`, which plans
# no changes. An imported entry needs steps only to be replaced. Write them into the configuration
# whenever you like, and the next apply records them without touching the entry.
tofu import homeassistant_integration.shelly 01JABCDEF0123456789ABCDEFG
