package resources_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// checkDefaultDashboardConfig fails unless the default dashboard's saved config means the same as
// want. A want of "" means it has none, i.e. it is auto-generated.
func checkDefaultDashboardConfig(t *testing.T, want string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if want == "" {
			_, err := dashboards(t).Config(context.Background(), client.DefaultDashboardURLPath)
			if !errors.Is(err, client.ErrNoDashboardConfig) {
				return fmt.Errorf("default dashboard: err = %v, want no saved config", err)
			}
			return nil
		}
		return checkDashboardConfig(t, client.DefaultDashboardURLPath, want)(nil)
	}
}

func TestAccDefaultDashboard(t *testing.T) {
	const addr = "homeassistant_default_dashboard.test"
	const edited = `{"views":[{"title":"Edited in the UI"}]}`
	cfg := func(title string) string {
		return acctest.ProviderConfig + `
resource "homeassistant_default_dashboard" "test" {
  config = {
    views = [{ type = "sections", title = "` + title + `", sections = [{ type = "grid", cards = [{ type = "tile", entity = "sun.sun" }] }] }]
  }
}
`
	}
	want := func(title string) string {
		return `{"views":[{"type":"sections","title":"` + title + `","sections":[{"type":"grid","cards":[{"type":"tile","entity":"sun.sun"}]}]}]}`
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Destroy resets the dashboard to the auto-generated one.
		CheckDestroy: checkDefaultDashboardConfig(t, ""),
		Steps: []resource.TestStep{
			{
				Config: cfg("Overview"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "default"),
					checkDefaultDashboardConfig(t, want("Overview")),
				),
			},
			{
				Config: cfg("Home"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkDefaultDashboardConfig(t, want("Home")),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "default",
				ImportStateVerify: true,
			},
			{
				// A config edited in the HA UI shows up as drift, and the apply puts it back.
				PreConfig: func() {
					if err := dashboards(t).SaveConfig(context.Background(), client.DefaultDashboardURLPath, json.RawMessage(edited)); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("Home"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkDefaultDashboardConfig(t, want("Home")),
			},
			{
				// Reset to auto-generated outside Tofu: the config reads back as null, and the
				// apply saves it again.
				PreConfig: func() {
					if err := dashboards(t).DeleteConfig(context.Background(), client.DefaultDashboardURLPath); err != nil {
						t.Fatal(err)
					}
				},
				Config: cfg("Home"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkDefaultDashboardConfig(t, want("Home")),
			},
		},
	})
}

// Importing the default dashboard that has a saved config, then planning the same config, shows
// no changes; creating over a saved config fails and leaves it alone.
func TestAccDefaultDashboard_existingConfig(t *testing.T) {
	const existing = `{"views":[{"title":"Made in the UI","cards":[{"type":"markdown","content":"hi"}]}]}`
	const addr = "homeassistant_default_dashboard.test"
	const body = `
resource "homeassistant_default_dashboard" "test" {
  config = {
    views = [{ title = "Made in the UI", cards = [{ type = "markdown", content = "hi" }] }]
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkDefaultDashboardConfig(t, ""),
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					if err := dashboards(t).SaveConfig(context.Background(), client.DefaultDashboardURLPath, json.RawMessage(existing)); err != nil {
						t.Fatal(err)
					}
				},
				Config:      acctest.ProviderConfig + body,
				ExpectError: regexp.MustCompile(`already has a config`),
			},
			{
				Config:             acctest.ProviderConfig + body,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      "default",
				ImportStatePersist: true,
			},
			{
				Config: acctest.ProviderConfig + body,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: checkDefaultDashboardConfig(t, existing),
			},
		},
	})
}
