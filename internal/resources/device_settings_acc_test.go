package resources_test

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// moonDevice creates a config entry of moon and returns the ID of its device. The entry is
// deleted when the test ends.
func moonDevice(t *testing.T) string {
	t.Helper()
	entityID := moonEntity(t)
	e, ok, err := client.Entities(haClient(t).WS).Get(context.Background(), entityID)
	if err != nil || !ok || e.DeviceID == nil {
		t.Fatalf("no device for %s: ok %v, err %v, entry %+v", entityID, ok, err, e)
	}
	return *e.DeviceID
}

// checkDevice runs check on the registry entry of deviceID, which must exist.
func checkDevice(t *testing.T, deviceID string, check func(client.Device) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		d, ok, err := client.Devices(haClient(t).WS).Get(context.Background(), deviceID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("device %s is gone", deviceID)
		}
		return check(d)
	}
}

func deviceSettingsConfig(deviceID, settings string) string {
	return acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_area" "sky" {
  name = "Acc Sky"
}

resource "homeassistant_label" "celestial" {
  name = "Acc Celestial"
}

resource "homeassistant_device_settings" "moon" {
  device_id = %q
%s
}
`, deviceID, settings)
}

func TestAccDeviceSettings(t *testing.T) {
	const addr = "homeassistant_device_settings.moon"
	deviceID := moonDevice(t)
	updateDevice := func(fields map[string]any) {
		if _, err := client.Devices(haClient(t).WS).Update(context.Background(), deviceID, fields); err != nil {
			t.Fatal(err)
		}
	}
	managed := `
  name_by_user = "Acc Night Light"
  area_id      = homeassistant_area.sky.id
  labels       = [homeassistant_label.celestial.id]
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Destroy resets only the configured fields and leaves the device in place.
		CheckDestroy: checkDevice(t, deviceID, func(d client.Device) error {
			if d.NameByUser != nil || d.AreaID != nil || len(d.Labels) != 0 || d.DisabledBy != nil {
				return fmt.Errorf("not reset: name_by_user %s, area %s, labels %v, disabled_by %s",
					deref(d.NameByUser), deref(d.AreaID), d.Labels, deref(d.DisabledBy))
			}
			return nil
		}),
		Steps: []resource.TestStep{
			{
				Config: deviceSettingsConfig(deviceID, managed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name_by_user", "Acc Night Light"),
					resource.TestCheckResourceAttr(addr, "area_id", "acc_sky"),
					resource.TestCheckResourceAttr(addr, "labels.#", "1"),
					resource.TestCheckNoResourceAttr(addr, "disabled"),
					resource.TestCheckResourceAttrSet(addr, "name"),
					resource.TestCheckResourceAttr(addr, "identifiers.#", "1"),
					resource.TestCheckResourceAttr(addr, "identifiers.0.0", "moon"),
					resource.TestCheckResourceAttr(addr, "config_entries.#", "1"),
					checkDevice(t, deviceID, func(d client.Device) error {
						if deref(d.NameByUser) != "Acc Night Light" || deref(d.AreaID) != "acc_sky" ||
							!slices.Equal(d.Labels, []string{"acc_celestial"}) {
							return fmt.Errorf("device = %+v", d)
						}
						return nil
					}),
				),
			},
			{
				// An attribute changed in the UI that is not in config causes no diff.
				PreConfig: func() { updateDevice(map[string]any{"disabled_by": "user"}) },
				Config:    deviceSettingsConfig(deviceID, managed),
				PlanOnly:  true,
			},
			{
				// A configured attribute changed in the UI is drift.
				PreConfig: func() { updateDevice(map[string]any{"name_by_user": "Changed in the UI"}) },
				Config:    deviceSettingsConfig(deviceID, managed),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkDevice(t, deviceID, func(d client.Device) error {
					if deref(d.NameByUser) != "Acc Night Light" {
						return fmt.Errorf("name_by_user = %s, want it restored", deref(d.NameByUser))
					}
					return nil
				}),
			},
			{
				// Import manages nothing until the configuration says so.
				ResourceName:                         addr,
				ImportState:                          true,
				ImportStateId:                        deviceID,
				ImportStateVerify:                    true,
				ImportStateVerifyIdentifierAttribute: "device_id",
				ImportStateVerifyIgnore:              []string{"name_by_user", "area_id", "labels"},
			},
			{
				// Dropping name_by_user resets it; disabled = false maps to null.
				Config: deviceSettingsConfig(deviceID, `
  area_id  = homeassistant_area.sky.id
  labels   = [homeassistant_label.celestial.id]
  disabled = false
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "name_by_user"),
					resource.TestCheckResourceAttr(addr, "disabled", "false"),
					checkDevice(t, deviceID, func(d client.Device) error {
						if d.NameByUser != nil || d.DisabledBy != nil {
							return fmt.Errorf("device = %+v", d)
						}
						return nil
					}),
				),
			},
			{
				// disabled = true maps to "user".
				Config: deviceSettingsConfig(deviceID, `
  area_id  = homeassistant_area.sky.id
  labels   = [homeassistant_label.celestial.id]
  disabled = true
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "disabled", "true"),
					checkDevice(t, deviceID, func(d client.Device) error {
						if deref(d.DisabledBy) != "user" {
							return fmt.Errorf("disabled_by = %s, want user", deref(d.DisabledBy))
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccDeviceSettings_Timeout(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_device_settings" "missing" {
  device_id = "acc_no_such_device"
  area_id   = "nowhere"
  timeouts = {
    create = "2s"
  }
}
`,
				ExpectError: regexp.MustCompile(`did\s+not\s+appear\s+in\s+the\s+device\s+registry\s+within\s+2s`),
			},
		},
	})
}

// A device and entity disabled through their config entry stay disabled: `disabled = false` reads
// as no drift, and neither create nor destroy clears a flag the user did not set. HA refuses to
// enable an entity of a disabled device, so writing `disabled_by: null` would fail.
func TestAccSettings_DisabledByConfigEntry(t *testing.T) {
	entityID := moonEntity(t)
	c := haClient(t)
	e, ok, err := client.Entities(c.WS).Get(context.Background(), entityID)
	if err != nil || !ok || e.DeviceID == nil {
		t.Fatalf("no device for %s: ok %v, err %v, entry %+v", entityID, ok, err, e)
	}
	deviceID := *e.DeviceID
	d, _, err := client.Devices(c.WS).Get(context.Background(), deviceID)
	if err != nil || len(d.ConfigEntries) != 1 {
		t.Fatalf("config entries of %s: %v, err %v", deviceID, d.ConfigEntries, err)
	}
	disabledByEntry := resource.ComposeAggregateTestCheckFunc(
		checkDevice(t, deviceID, func(d client.Device) error {
			if deref(d.DisabledBy) != "config_entry" {
				return fmt.Errorf("device disabled_by = %s, want config_entry", deref(d.DisabledBy))
			}
			return nil
		}),
		checkEntityEntry(t, entityID, func(e client.EntityEntry) error {
			if deref(e.DisabledBy) != "config_entry" {
				return fmt.Errorf("entity disabled_by = %s, want config_entry", deref(e.DisabledBy))
			}
			return nil
		}),
	)
	config := acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_device_settings" "moon" {
  device_id = %q
  disabled  = false
}

resource "homeassistant_entity_settings" "moon" {
  entity_id = %q
  disabled  = false
}
`, deviceID, entityID)
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			acctest.PreCheck(t)
			err := c.WS.Command(context.Background(), "config_entries/disable",
				map[string]any{"entry_id": d.ConfigEntries[0], "disabled_by": "user"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := disabledByEntry(nil); err != nil {
				t.Fatal(err)
			}
		},
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             disabledByEntry,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("homeassistant_device_settings.moon", "disabled", "false"),
					resource.TestCheckResourceAttr("homeassistant_entity_settings.moon", "disabled", "false"),
					disabledByEntry,
				),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
		},
	})
}

func TestAccDeviceSettings_upgrade(t *testing.T) {
	const addr = "homeassistant_device_settings.moon"
	acctest.UpgradeTest(t, deviceSettingsConfig(moonDevice(t), `
  name_by_user = "Acc Night Light"
  area_id      = homeassistant_area.sky.id
  labels       = [homeassistant_label.celestial.id]
`),
		resource.TestCheckResourceAttr(addr, "name_by_user", "Acc Night Light"),
		resource.TestCheckResourceAttr(addr, "area_id", "acc_sky"),
		resource.TestCheckResourceAttr(addr, "identifiers.0.0", "moon"),
	)
}
