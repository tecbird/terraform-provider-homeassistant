package resources_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
)

// The menu tests use the template helper: its config flow starts with a menu of entity types, needs
// no hardware or network, and allows several entries, one per name.

// checkTemplatesGone fails if HA has a template entry with one of the titles.
func checkTemplatesGone(t *testing.T, titles ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		entries, err := configEntries(t).List(context.Background(), "template")
		if err != nil {
			return err
		}
		for _, e := range entries {
			for _, title := range titles {
				if e.Title == title {
					t.Errorf("config entry %q (%s) still exists", title, e.EntryID)
				}
			}
		}
		return nil
	}
}

// wrapped matches text in a diagnostic, which OpenTofu wraps at any space.
func wrapped(text string) *regexp.Regexp {
	return regexp.MustCompile(strings.Join(strings.Fields(regexp.QuoteMeta(text)), `\s+`))
}

// templateSensorConfig is a homeassistant_integration "menu" that picks `sensor` in the menu.
func templateSensorConfig(name string) string {
	return acctest.ProviderConfig + `
resource "homeassistant_integration" "menu" {
  domain = "template"
  steps = {
    user   = { next_step_id = "sensor" }
    sensor = { name = "` + name + `", state = "{{ 6 * 7 }}" }
  }
}
`
}

func TestAccIntegration_Menu(t *testing.T) {
	const addr = "homeassistant_integration.menu"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkTemplatesGone(t, "Acc Menu"),
		Steps: []resource.TestStep{
			{
				Config: templateSensorConfig("Acc Menu"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "domain", "template"),
					// The title is the name from the sensor form.
					resource.TestCheckResourceAttr(addr, "title", "Acc Menu"),
					resource.TestCheckResourceAttr(addr, "state", "loaded"),
					func(*terraform.State) error {
						// The menu chose a sensor. Its state is not checked: in a fresh HA 2026.10 it
						// was still "unknown" 30s after the entry was created.
						acctest.Eventually(t, 30*time.Second, "sensor.acc_menu exists", func() (bool, error) {
							_, err := haClient(t).REST.State(context.Background(), "sensor.acc_menu")
							return err == nil, err
						})
						return nil
					},
				),
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"steps", "sensitive_steps"},
			},
			{
				Config: templateSensorConfig("Acc Menu"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccIntegration_MenuChoiceErrors(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// An option the menu does not have; the flow is aborted before anything is created.
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration" "wrong_option" {
  domain = "template"
  steps = {
    user   = { next_step_id = "no_such_type" }
    sensor = { name = "Acc Wrong Option", state = "1" }
  }
}
`,
				ExpectError: wrapped(`config flow for template showed the menu "user", which has no option "no_such_type"; the options are`),
			},
			{
				// The menu's step without next_step_id.
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration" "no_choice" {
  domain = "template"
  steps = {
    user = { name = "Acc No Choice", state = "1" }
  }
}
`,
				ExpectError: wrapped(`showed the menu "user", which takes only next_step_id, but the step also has "name", "state"`),
			},
			{
				Config: acctest.ProviderConfig,
				Check:  checkTemplatesGone(t, "Acc Wrong Option", "Acc No Choice"),
			},
		},
	})
}
