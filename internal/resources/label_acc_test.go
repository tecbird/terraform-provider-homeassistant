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

// labels returns the label registry of the shared instance, for setting up and checking what
// the provider does.
func labels(t *testing.T) client.Registry[client.Label] {
	t.Helper()
	return client.Labels(haClient(t).WS)
}

// checkLabelGone fails if HA has a label with the given ID.
func checkLabelGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, ok, err := labels(t).Get(context.Background(), id); err != nil || ok {
			t.Errorf("label %q: exists = %v, err = %v; want it gone", id, ok, err)
		}
		return nil
	}
}

func TestAccLabel(t *testing.T) {
	const addr = "homeassistant_label.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkLabelGone(t, "acc_label")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "test" {
  name  = "Acc Label"
  color = "indigo"
  icon  = "mdi:tag"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_label"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Label"),
					resource.TestCheckResourceAttr(addr, "color", "indigo"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:tag"),
					resource.TestCheckNoResourceAttr(addr, "description"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_label",
				ImportStateVerify: true,
			},
			{
				// Rename in place; the ID stays. Dropping icon clears it.
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "test" {
  name        = "Acc Label Renamed"
  color       = "#ff8800"
  description = "Managed by OpenTofu"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_label"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Label Renamed"),
					resource.TestCheckResourceAttr(addr, "color", "#ff8800"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					resource.TestCheckResourceAttr(addr, "description", "Managed by OpenTofu"),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_label",
				ImportStateVerify: true,
			},
			{
				// Deleted outside Tofu: the label drops out of state and is created again.
				PreConfig: func() {
					if err := labels(t).Delete(context.Background(), "acc_label"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "test" {
  name        = "Acc Label Renamed"
  color       = "#ff8800"
  description = "Managed by OpenTofu"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				// HA derives the new ID from the current name.
				Check: resource.TestCheckResourceAttr(addr, "id", "acc_label_renamed"),
			},
		},
	})
}

func TestAccLabel_ExplicitID(t *testing.T) {
	const addr = "homeassistant_label.pinned"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             func(s *terraform.State) error { return checkLabelGone(t, "acc_pinned_2")(s) },
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "pinned" {
  id   = "acc_pinned"
  name = "Pinned Label"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_pinned"),
					resource.TestCheckResourceAttr(addr, "name", "Pinned Label"),
					func(*terraform.State) error {
						l, ok, err := labels(t).Get(context.Background(), "acc_pinned")
						if err != nil || !ok || l.Name != "Pinned Label" {
							t.Errorf("label in HA = %+v, found %v, err %v; want name %q", l, ok, err, "Pinned Label")
						}
						return nil
					},
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_pinned",
				ImportStateVerify: true,
			},
			{
				// Changing the ID replaces the label.
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "pinned" {
  id   = "acc_pinned_2"
  name = "Pinned Label"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_pinned_2"),
					checkLabelGone(t, "acc_pinned"),
				),
			},
			{
				// Removing `id` from the config keeps the label.
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "pinned" {
  name = "Pinned Label"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// createLabel creates a label directly in HA and deletes it when the test ends.
func createLabel(t *testing.T, name string) client.Label {
	t.Helper()
	ctx := context.Background()
	l, err := labels(t).Create(ctx, map[string]any{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := labels(t).Delete(ctx, l.LabelID); err != nil {
			t.Error(err)
		}
	})
	return l
}

func TestAccLabel_IDCollision(t *testing.T) {
	acctest.SharedInstance(t)
	// An existing label holds the ID acc_clash under another name, so HA would give a new
	// "Acc Clash" the ID acc_clash_2.
	existing := createLabel(t, "Acc Clash")
	if _, err := labels(t).Update(context.Background(), existing.LabelID, map[string]any{"name": "Acc Clash Old"}); err != nil {
		t.Fatal(err)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "clash" {
  name = "Acc Clash"
}
`,
				ExpectError: regexp.MustCompile(`(?s)a label with the ID "acc_clash" already exists.*tofu import`),
			},
			{
				// Nothing to apply; only check that HA kept no acc_clash_2.
				Config: acctest.ProviderConfig,
				Check:  checkLabelGone(t, "acc_clash_2"),
			},
		},
	})
}

func TestAccLabel_NameInUse(t *testing.T) {
	acctest.SharedInstance(t)
	createLabel(t, "Acc Duplicate")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_label" "dup" {
  name = "Acc Duplicate"
}
`,
				ExpectError: regexp.MustCompile(`(?s)already in use.*import it`),
			},
			{
				Config: acctest.ProviderConfig,
				Check:  checkLabelGone(t, "acc_duplicate_2"),
			},
		},
	})
}

func TestAccLabel_upgrade(t *testing.T) {
	const addr = "homeassistant_label.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_label" "test" {
  name  = "Acc Label"
  color = "indigo"
  icon  = "mdi:tag"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_label"),
		resource.TestCheckResourceAttr(addr, "color", "indigo"),
	)
}
