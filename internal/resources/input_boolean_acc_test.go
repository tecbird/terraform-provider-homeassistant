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

// inputBooleans returns the input_boolean collection of the shared instance.
func inputBooleans(t *testing.T) client.Registry[client.Helper] {
	t.Helper()
	return client.Helpers(haClient(t).WS, "input_boolean")
}

// checkInputBooleanGone fails if HA has an input_boolean with the given ID.
func checkInputBooleanGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, ok, err := inputBooleans(t).Get(context.Background(), id); err != nil || ok {
			t.Errorf("input_boolean %q: exists = %v, err = %v; want it gone", id, ok, err)
		}
		return nil
	}
}

func TestAccInputBoolean(t *testing.T) {
	const addr = "homeassistant_input_boolean.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkInputBooleanGone(t, "acc_toggle_renamed")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "test" {
  name    = "Acc Toggle"
  icon    = "mdi:toggle-switch"
  initial = true
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_toggle"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_toggle"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Toggle"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:toggle-switch"),
					resource.TestCheckResourceAttr(addr, "initial", "true"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_toggle",
				ImportStateVerify: true,
			},
			{
				// Rename in place; the ID stays. Dropping icon and initial clears them.
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "test" {
  name = "Acc Toggle Renamed"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_toggle"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_toggle"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Toggle Renamed"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					resource.TestCheckNoResourceAttr(addr, "initial"),
					func(*terraform.State) error {
						h, ok, err := inputBooleans(t).Get(context.Background(), "acc_toggle")
						if err != nil || !ok || h["icon"] != nil || h["initial"] != nil {
							t.Errorf("input_boolean in HA = %v, found %v, err %v; want icon and initial cleared", h, ok, err)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_toggle",
				ImportStateVerify: true,
			},
			{
				// Deleted outside Tofu: the helper drops out of state and is created again.
				PreConfig: func() {
					if err := inputBooleans(t).Delete(context.Background(), "acc_toggle"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "test" {
  name = "Acc Toggle Renamed"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_toggle_renamed"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_toggle_renamed"),
				),
			},
		},
	})
}

func TestAccInputBoolean_ExplicitID(t *testing.T) {
	const addr = "homeassistant_input_boolean.pinned"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkInputBooleanGone(t, "acc_pinned_bool_2")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "pinned" {
  id      = "acc_pinned_bool"
  name    = "Pinned Toggle"
  initial = false
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_pinned_bool"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_pinned_bool"),
					resource.TestCheckResourceAttr(addr, "name", "Pinned Toggle"),
					resource.TestCheckResourceAttr(addr, "initial", "false"),
					func(*terraform.State) error {
						h, ok, err := inputBooleans(t).Get(context.Background(), "acc_pinned_bool")
						if err != nil || !ok || h["name"] != "Pinned Toggle" || h["initial"] != false {
							t.Errorf("input_boolean in HA = %v, found %v, err %v; want name %q, initial false", h, ok, err, "Pinned Toggle")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_pinned_bool",
				ImportStateVerify: true,
			},
			{
				// Changing the ID replaces the helper.
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "pinned" {
  id      = "acc_pinned_bool_2"
  name    = "Pinned Toggle"
  initial = false
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_pinned_bool_2"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_pinned_bool_2"),
					checkInputBooleanGone(t, "acc_pinned_bool"),
				),
			},
			{
				// Removing `id` from the config keeps the helper.
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "pinned" {
  name    = "Pinned Toggle"
  initial = false
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccInputBoolean_IDCollision(t *testing.T) {
	acctest.SharedInstance(t)
	// An existing helper holds the ID acc_bool_clash under another name, so HA would give a new
	// "Acc Bool Clash" the ID acc_bool_clash_2.
	ctx := context.Background()
	existing, err := inputBooleans(t).Create(ctx, map[string]any{"name": "Acc Bool Clash"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := inputBooleans(t).Delete(ctx, existing.HelperID()); err != nil {
			t.Error(err)
		}
	})
	if _, err := inputBooleans(t).Update(ctx, existing.HelperID(), map[string]any{"name": "Acc Bool Clash Old"}); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_boolean" "clash" {
  name = "Acc Bool Clash"
}
`,
				ExpectError: regexp.MustCompile(`(?s)a helper with the ID "acc_bool_clash" already exists.*tofu import homeassistant_input_boolean`),
			},
			{
				// Nothing to apply; only check that HA kept no acc_bool_clash_2.
				Config: acctest.ProviderConfig,
				Check:  checkInputBooleanGone(t, "acc_bool_clash_2"),
			},
		},
	})
}

func TestAccInputBoolean_upgrade(t *testing.T) {
	const addr = "homeassistant_input_boolean.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_boolean" "test" {
  name    = "Acc Toggle"
  icon    = "mdi:toggle-switch"
  initial = true
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_toggle"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_boolean.acc_toggle"),
	)
}
