package resources_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// floors returns the floor registry of the shared instance, for setting up and checking what
// the provider does.
func floors(t *testing.T) client.Registry[client.Floor] {
	t.Helper()
	return client.Floors(haClient(t).WS)
}

// checkFloorGone fails if HA has a floor with the given ID.
func checkFloorGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, ok, err := floors(t).Get(context.Background(), id); err != nil || ok {
			t.Errorf("floor %q: exists = %v, err = %v; want it gone", id, ok, err)
		}
		return nil
	}
}

func TestAccFloor(t *testing.T) {
	const addr = "homeassistant_floor.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkFloorGone(t, "acc_floor")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "test" {
  name    = "Acc Floor"
  level   = 0
  icon    = "mdi:home-floor-0"
  aliases = ["Ground", "EG"]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_floor"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Floor"),
					resource.TestCheckResourceAttr(addr, "level", "0"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:home-floor-0"),
					resource.TestCheckResourceAttr(addr, "aliases.#", "2"),
					resource.TestCheckTypeSetElemAttr(addr, "aliases.*", "Ground"),
					resource.TestCheckTypeSetElemAttr(addr, "aliases.*", "EG"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_floor",
				ImportStateVerify: true,
			},
			{
				// Same aliases in another order: a set, so no change.
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "test" {
  name    = "Acc Floor"
  level   = 0
  icon    = "mdi:home-floor-0"
  aliases = ["EG", "Ground"]
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Rename in place; the ID stays. Dropping icon and aliases clears them.
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "test" {
  name  = "Acc Floor Renamed"
  level = -1
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_floor"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Floor Renamed"),
					resource.TestCheckResourceAttr(addr, "level", "-1"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					resource.TestCheckResourceAttr(addr, "aliases.#", "0"),
					func(*terraform.State) error {
						f, ok, err := floors(t).Get(context.Background(), "acc_floor")
						if err != nil || !ok || f.Icon != nil || len(f.Aliases) != 0 {
							t.Errorf("floor in HA = %+v, found %v, err %v; want icon and aliases cleared", f, ok, err)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_floor",
				ImportStateVerify: true,
			},
			{
				// Removing level clears it.
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "test" {
  name = "Acc Floor Renamed"
}
`,
				Check: resource.TestCheckNoResourceAttr(addr, "level"),
			},
			{
				// Deleted outside Tofu: the floor drops out of state and is created again.
				PreConfig: func() {
					if err := floors(t).Delete(context.Background(), "acc_floor"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "test" {
  name = "Acc Floor Renamed"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				// HA derives the new ID from the current name.
				Check: resource.TestCheckResourceAttr(addr, "id", "acc_floor_renamed"),
			},
		},
	})
}

func TestAccFloor_ExplicitID(t *testing.T) {
	const addr = "homeassistant_floor.pinned"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkFloorGone(t, "acc_upstairs_2")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "pinned" {
  id    = "acc_upstairs"
  name  = "First Floor"
  level = 1
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_upstairs"),
					resource.TestCheckResourceAttr(addr, "name", "First Floor"),
					func(*terraform.State) error {
						f, ok, err := floors(t).Get(context.Background(), "acc_upstairs")
						if err != nil || !ok || f.Name != "First Floor" || f.Level == nil || *f.Level != 1 {
							t.Errorf("floor in HA = %+v, found %v, err %v; want name %q, level 1", f, ok, err, "First Floor")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_upstairs",
				ImportStateVerify: true,
			},
			{
				// Changing the ID replaces the floor.
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "pinned" {
  id    = "acc_upstairs_2"
  name  = "First Floor"
  level = 1
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_upstairs_2"),
					checkFloorGone(t, "acc_upstairs"),
				),
			},
		},
	})
}

func TestAccFloor_IDCollision(t *testing.T) {
	acctest.SharedInstance(t)
	// An existing floor holds the ID acc_floor_clash under another name, so HA would give a new
	// "Acc Floor Clash" the ID acc_floor_clash_2.
	ctx := context.Background()
	existing, err := floors(t).Create(ctx, map[string]any{"name": "Acc Floor Clash"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := floors(t).Delete(ctx, existing.FloorID); err != nil {
			t.Error(err)
		}
	})
	if _, err := floors(t).Update(ctx, existing.FloorID, map[string]any{"name": "Acc Floor Clash Old"}); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_floor" "clash" {
  name = "Acc Floor Clash"
}
`,
				ExpectError: regexp.MustCompile(`(?s)a floor with the ID "acc_floor_clash" already exists.*tofu import`),
			},
			{
				Config: acctest.ProviderConfig,
				Check:  checkFloorGone(t, "acc_floor_clash_2"),
			},
		},
	})
}

func TestAccFloor_upgrade(t *testing.T) {
	const addr = "homeassistant_floor.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_floor" "test" {
  name    = "Acc Floor"
  level   = 0
  icon    = "mdi:home-floor-0"
  aliases = ["Ground", "EG"]
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_floor"),
		resource.TestCheckResourceAttr(addr, "aliases.#", "2"),
	)
}
