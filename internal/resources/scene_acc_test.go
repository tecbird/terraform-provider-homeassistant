package resources_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func scenes(t *testing.T) client.ConfigItems {
	t.Helper()
	return client.NewScenes(haClient(t).REST)
}

// checkSceneConfig fails unless HA's stored config of the scene means the same as want.
func checkSceneConfig(t *testing.T, id, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := scenes(t).Get(context.Background(), id)
		if err != nil {
			return err
		}
		eq, err := dyntype.SemanticallyEqualJSON(got, []byte(want))
		if err != nil {
			return err
		}
		if !eq {
			return fmt.Errorf("scene %q config = %s, want %s", id, got, want)
		}
		return nil
	}
}

func checkSceneGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, err := scenes(t).Get(context.Background(), id); !errors.Is(err, client.ErrNotFound) {
			return fmt.Errorf("scene %q: err = %w; want it gone", id, err)
		}
		return nil
	}
}

// accScene sets an input_boolean that the same config creates.
const accScene = `
resource "homeassistant_input_boolean" "tv" {
  id   = "acc_scene_tv"
  name = "Acc Scene TV"
}

resource "homeassistant_scene" "test" {
  id = "acc_movie"
  config = {
    name = %q
    icon = "mdi:movie"
    entities = {
      "input_boolean.acc_scene_tv" = { state = "on", friendly_name = "Acc Scene TV" }
    }
  }
  depends_on = [homeassistant_input_boolean.tv]
}
`

const accSceneStored = `{"name":%q,"icon":"mdi:movie",
	"entities":{"input_boolean.acc_scene_tv":{"state":"on","friendly_name":"Acc Scene TV"}}}`

func TestAccScene(t *testing.T) {
	const addr = "homeassistant_scene.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkSceneGone(t, "acc_movie"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(accScene, "Acc Movie Night"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_movie"),
					// The entity ID comes from the name, not the id.
					resource.TestCheckResourceAttr(addr, "entity_id", "scene.acc_movie_night"),
					checkSceneConfig(t, "acc_movie", fmt.Sprintf(accSceneStored, "Acc Movie Night")),
				),
			},
			{
				// After a refresh, the plan is still empty.
				Config: acctest.ProviderConfig + fmt.Sprintf(accScene, "Acc Movie Night"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_movie",
				ImportStateVerify: true,
			},
			{
				// A new name updates in place and keeps the entity ID.
				Config: acctest.ProviderConfig + fmt.Sprintf(accScene, "Acc Cinema"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "entity_id", "scene.acc_movie_night"),
					checkSceneConfig(t, "acc_movie", fmt.Sprintf(accSceneStored, "Acc Cinema")),
				),
			},
			{
				// A change saved in the scene editor shows up as drift, and the apply puts the
				// config back.
				PreConfig: func() {
					if err := scenes(t).Save(context.Background(), "acc_movie", json.RawMessage(
						`{"name":"Edited in the UI","entities":{"input_boolean.acc_scene_tv":{"state":"off"}}}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accScene, "Acc Cinema"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkSceneConfig(t, "acc_movie", fmt.Sprintf(accSceneStored, "Acc Cinema")),
			},
			{
				// Deleted outside Tofu: the scene drops out of state and is created again, with an
				// entity ID from its current name.
				PreConfig: func() {
					if err := scenes(t).Delete(context.Background(), "acc_movie"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accScene, "Acc Cinema"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.TestCheckResourceAttrSet(addr, "entity_id"),
			},
		},
	})
}

// accSceneYAML is a scene as the scene editor's "Edit in YAML" shows it, without its id line.
const accSceneYAML = `name: Acc Evening
entities:
  input_boolean.acc_scene_evening:
    editable: true
    friendly_name: Acc Evening
    state: "off"
icon: mdi:weather-sunset
metadata:
  input_boolean.acc_scene_evening:
    entity_only: true
`

func TestAccScene_YAMLRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "evening.yaml")
	if err := os.WriteFile(file, []byte(accSceneYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	config := acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_scene" "test" {
  id     = "acc_evening"
  config = yamldecode(file(%q))
}
`, file)
	const addr = "homeassistant_scene.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkSceneGone(t, "acc_evening"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr(addr, "entity_id", "scene.acc_evening"),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccScene_InvalidConfig(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkSceneGone(t, "acc_invalid_scene"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_scene" "test" {
  id = "acc_invalid_scene"
  config = {
    name     = "Acc Invalid"
    entities = { "not an entity id" = { state = "on" } }
  }
}
`,
				ExpectError: regexp.MustCompile(`HTTP 400`),
			},
		},
	})
}

func TestAccScene_upgrade(t *testing.T) {
	const addr = "homeassistant_scene.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+fmt.Sprintf(accScene, "Acc Movie Night"),
		resource.TestCheckResourceAttr(addr, "id", "acc_movie"),
		resource.TestCheckResourceAttr(addr, "entity_id", "scene.acc_movie_night"),
	)
}
