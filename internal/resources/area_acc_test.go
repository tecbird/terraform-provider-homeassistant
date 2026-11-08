package resources_test

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// areas returns the area registry of the shared instance, for setting up and checking what
// the provider does.
func areas(t *testing.T) client.Registry[client.Area] {
	t.Helper()
	return client.Areas(haClient(t).WS)
}

// checkAreaGone fails if HA has an area with the given ID.
func checkAreaGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, ok, err := areas(t).Get(context.Background(), id); err != nil || ok {
			t.Errorf("area %q: exists = %v, err = %v; want it gone", id, ok, err)
		}
		return nil
	}
}

// postSensor sets the state of a sensor with the given device class, which is what HA requires
// of an area's temperature and humidity entity. The state is removed when the test ends.
func postSensor(t *testing.T, entityID, deviceClass, unit string) {
	t.Helper()
	ctx := context.Background()
	rest := haClient(t).REST
	body := map[string]any{
		"state":      "21",
		"attributes": map[string]any{"device_class": deviceClass, "unit_of_measurement": unit},
	}
	if err := rest.Do(ctx, http.MethodPost, "states/"+entityID, body, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := rest.Do(ctx, http.MethodDelete, "states/"+entityID, nil, nil); err != nil {
			t.Error(err)
		}
	})
}

// areaDeps are the floors and the label that the area tests refer to.
const areaDeps = `
resource "homeassistant_floor" "a" {
  name = "Acc Area Floor A"
}

resource "homeassistant_floor" "b" {
  name = "Acc Area Floor B"
}

resource "homeassistant_label" "test" {
  name = "Acc Area Label"
}
`

// areaFull sets every attribute of the area, with the given name, floor resource, and aliases.
func areaFull(name, floor, aliases string) string {
	return acctest.ProviderConfig + areaDeps + `
resource "homeassistant_area" "test" {
  name                  = "` + name + `"
  floor_id              = homeassistant_floor.` + floor + `.id
  icon                  = "mdi:sofa"
  aliases               = ` + aliases + `
  labels                = [homeassistant_label.test.id]
  picture               = "/local/acc_area.jpg"
  temperature_entity_id = "sensor.acc_area_temperature"
  humidity_entity_id    = "sensor.acc_area_humidity"
}
`
}

func TestAccArea(t *testing.T) {
	const addr = "homeassistant_area.test"
	acctest.SharedInstance(t)
	postSensor(t, "sensor.acc_area_temperature", "temperature", "°C")
	postSensor(t, "sensor.acc_area_humidity", "humidity", "%")
	bare := acctest.ProviderConfig + areaDeps + `
resource "homeassistant_area" "test" {
  name = "Acc Area Renamed"
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkAreaGone(t, "acc_area_renamed")(s) },
		Steps: []resource.TestStep{
			{
				Config: areaFull("Acc Area", "a", `["Lounge", "Den"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_area"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Area"),
					resource.TestCheckResourceAttr(addr, "floor_id", "acc_area_floor_a"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:sofa"),
					resource.TestCheckResourceAttr(addr, "aliases.#", "2"),
					resource.TestCheckTypeSetElemAttr(addr, "aliases.*", "Lounge"),
					resource.TestCheckTypeSetElemAttr(addr, "aliases.*", "Den"),
					resource.TestCheckResourceAttr(addr, "labels.#", "1"),
					resource.TestCheckTypeSetElemAttr(addr, "labels.*", "acc_area_label"),
					resource.TestCheckResourceAttr(addr, "picture", "/local/acc_area.jpg"),
					resource.TestCheckResourceAttr(addr, "temperature_entity_id", "sensor.acc_area_temperature"),
					resource.TestCheckResourceAttr(addr, "humidity_entity_id", "sensor.acc_area_humidity"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_area",
				ImportStateVerify: true,
			},
			{
				// Same aliases in another order: a set, so no change.
				Config: areaFull("Acc Area", "a", `["Den", "Lounge"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Moving to another floor and renaming are in-place updates; the ID stays.
				Config: areaFull("Acc Area Renamed", "b", `["Den", "Lounge"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_area"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Area Renamed"),
					resource.TestCheckResourceAttr(addr, "floor_id", "acc_area_floor_b"),
					func(*terraform.State) error {
						a, ok, err := areas(t).Get(context.Background(), "acc_area")
						if err != nil || !ok || a.Name != "Acc Area Renamed" || a.FloorID == nil ||
							*a.FloorID != "acc_area_floor_b" {
							t.Errorf("area in HA = %+v, found %v, err %v; want it renamed and on acc_area_floor_b", a, ok, err)
						}
						return nil
					},
				),
			},
			{
				// Dropping the optional attributes clears them.
				Config: bare,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_area"),
					resource.TestCheckNoResourceAttr(addr, "floor_id"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					resource.TestCheckResourceAttr(addr, "aliases.#", "0"),
					resource.TestCheckResourceAttr(addr, "labels.#", "0"),
					resource.TestCheckNoResourceAttr(addr, "picture"),
					resource.TestCheckNoResourceAttr(addr, "temperature_entity_id"),
					resource.TestCheckNoResourceAttr(addr, "humidity_entity_id"),
					func(*terraform.State) error {
						a, ok, err := areas(t).Get(context.Background(), "acc_area")
						if err != nil || !ok || a.FloorID != nil || a.Icon != nil || len(a.Aliases) != 0 ||
							len(a.Labels) != 0 || a.Picture != nil || a.TemperatureEntityID != nil ||
							a.HumidityEntityID != nil {
							t.Errorf("area in HA = %+v, found %v, err %v; want all but the name cleared", a, ok, err)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_area",
				ImportStateVerify: true,
			},
			{
				// Deleted outside Tofu: the area drops out of state and is created again.
				PreConfig: func() {
					if err := areas(t).Delete(context.Background(), "acc_area"); err != nil {
						t.Fatal(err)
					}
				},
				Config: bare,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				// HA derives the new ID from the current name.
				Check: resource.TestCheckResourceAttr(addr, "id", "acc_area_renamed"),
			},
		},
	})
}

func TestAccArea_ExplicitID(t *testing.T) {
	const addr = "homeassistant_area.pinned"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkAreaGone(t, "acc_study_2")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_area" "pinned" {
  id   = "acc_study"
  name = "Home Office"
  icon = "mdi:desk"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_study"),
					resource.TestCheckResourceAttr(addr, "name", "Home Office"),
					func(*terraform.State) error {
						a, ok, err := areas(t).Get(context.Background(), "acc_study")
						if err != nil || !ok || a.Name != "Home Office" || a.Icon == nil || *a.Icon != "mdi:desk" {
							t.Errorf("area in HA = %+v, found %v, err %v; want name %q, icon mdi:desk", a, ok, err, "Home Office")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_study",
				ImportStateVerify: true,
			},
			{
				// Changing the ID replaces the area.
				Config: acctest.ProviderConfig + `
resource "homeassistant_area" "pinned" {
  id   = "acc_study_2"
  name = "Home Office"
  icon = "mdi:desk"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_study_2"),
					checkAreaGone(t, "acc_study"),
				),
			},
		},
	})
}

func TestAccArea_IDCollision(t *testing.T) {
	acctest.SharedInstance(t)
	// An existing area holds the ID acc_area_clash under another name, so HA would give a new
	// "Acc Area Clash" the ID acc_area_clash_2.
	ctx := context.Background()
	existing, err := areas(t).Create(ctx, map[string]any{"name": "Acc Area Clash"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := areas(t).Delete(ctx, existing.AreaID); err != nil {
			t.Error(err)
		}
	})
	if _, err := areas(t).Update(ctx, existing.AreaID, map[string]any{"name": "Acc Area Clash Old"}); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_area" "clash" {
  name = "Acc Area Clash"
}
`,
				ExpectError: regexp.MustCompile(`(?s)an area with the ID "acc_area_clash" already exists.*tofu import`),
			},
			{
				Config: acctest.ProviderConfig,
				Check:  checkAreaGone(t, "acc_area_clash_2"),
			},
		},
	})
}

func TestAccArea_upgrade(t *testing.T) {
	const addr = "homeassistant_area.test"
	acctest.SharedInstance(t)
	postSensor(t, "sensor.acc_area_temperature", "temperature", "°C")
	postSensor(t, "sensor.acc_area_humidity", "humidity", "%")
	acctest.UpgradeTest(t, areaFull("Acc Area", "a", `["Lounge", "Den"]`),
		resource.TestCheckResourceAttr(addr, "id", "acc_area"),
		resource.TestCheckResourceAttr(addr, "floor_id", "acc_area_floor_a"),
		resource.TestCheckResourceAttr(addr, "labels.#", "1"),
	)
}
