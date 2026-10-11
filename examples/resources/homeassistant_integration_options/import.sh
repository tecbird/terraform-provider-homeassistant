# Import the options of a config entry by its entry_id. The import reads every field of the
# options flow's first form into `steps`, so `tofu plan -generate-config-out=options.tf` writes a
# configuration that plans no changes. Leaving fields out later only stops managing them.
tofu import homeassistant_integration_options.frost 01JABCDEF0123456789ABCDEFG
