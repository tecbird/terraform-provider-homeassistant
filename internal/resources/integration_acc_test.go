package resources_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// The tests use local_calendar: its config flow needs no hardware or network, works the same in
// every release of the support window, and allows several entries, one per calendar name.

// configEntries returns the config-entry API of the shared instance.
func configEntries(t *testing.T) client.ConfigEntries {
	t.Helper()
	c := haClient(t)
	return client.NewConfigEntries(c.WS, c.REST)
}

// calendarEntry returns the local_calendar entry with the given title, if there is one.
func calendarEntry(t *testing.T, title string) (client.ConfigEntry, bool) {
	t.Helper()
	entries, err := configEntries(t).List(context.Background(), "local_calendar")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Title == title {
			return e, true
		}
	}
	return client.ConfigEntry{}, false
}

// checkCalendarsGone fails if HA has a local_calendar entry with one of the titles.
func checkCalendarsGone(t *testing.T, titles ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, title := range titles {
			if e, ok := calendarEntry(t, title); ok {
				t.Errorf("config entry %q (%s) still exists", title, e.EntryID)
			}
		}
		return nil
	}
}

// calendarConfig is a homeassistant_integration "test" for a local calendar. The name is sent as
// sensitive data, which local_calendar does not need, but which shows the merge.
func calendarConfig(name string) string {
	return acctest.ProviderConfig + `
resource "homeassistant_integration" "test" {
  domain = "local_calendar"
  steps = {
    user = { import = "create_empty" }
  }
  sensitive_steps = {
    user = { calendar_name = "` + name + `" }
  }
}
`
}

func TestAccIntegration(t *testing.T) {
	const addr = "homeassistant_integration.test"
	var firstID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkCalendarsGone(t, "Acc Chores", "Acc Trash"),
		Steps: []resource.TestStep{
			{
				Config: calendarConfig("Acc Chores"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectSensitiveValue(addr, tfjsonpath.New("sensitive_steps")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "domain", "local_calendar"),
					resource.TestCheckResourceAttr(addr, "title", "Acc Chores"),
					resource.TestCheckResourceAttr(addr, "state", "loaded"),
					resource.TestCheckNoResourceAttr(addr, "disabled_by"),
					func(s *terraform.State) error {
						firstID = s.RootModule().Resources[addr].Primary.ID
						e, ok := calendarEntry(t, "Acc Chores")
						if !ok || e.EntryID != firstID {
							t.Errorf("entry in HA = %+v, found %v; want entry_id %s", e, ok, firstID)
						}
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
				// Same steps: nothing to do, although HA cannot return them.
				Config: calendarConfig("Acc Chores"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Changed steps replace the entry.
				Config: calendarConfig("Acc Trash"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "title", "Acc Trash"),
					checkCalendarsGone(t, "Acc Chores"),
					func(s *terraform.State) error {
						if id := s.RootModule().Resources[addr].Primary.ID; id == firstID {
							t.Errorf("entry_id %s did not change", id)
						}
						return nil
					},
				),
			},
			{
				// Deleted outside Tofu: the entry drops out of state and is created again.
				PreConfig: func() {
					e, ok := calendarEntry(t, "Acc Trash")
					if !ok {
						t.Fatal("entry Acc Trash is missing")
					}
					if _, err := configEntries(t).Delete(context.Background(), e.EntryID); err != nil {
						t.Fatal(err)
					}
				},
				Config: calendarConfig("Acc Trash"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "title", "Acc Trash"),
			},
		},
	})
}

// calendarOutsideTofu creates a local_calendar entry outside Tofu, as the UI would, deletes it when
// the test ends, and returns its entry_id.
func calendarOutsideTofu(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	id, err := client.NewConfigFlows(haClient(t).REST).Run(ctx, "local_calendar", "", []client.FlowStep{
		{StepID: "user", Data: map[string]any{"calendar_name": name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := configEntries(t).Delete(ctx, id); err != nil {
			t.Error(err)
		}
	})
	return id
}

func TestAccIntegration_Import(t *testing.T) {
	acctest.SharedInstance(t)
	const addr = "homeassistant_integration.imported"
	id := calendarOutsideTofu(t, "Acc Imported")

	config := acctest.ProviderConfig + `
resource "homeassistant_integration" "imported" {
  domain = "local_calendar"
  steps = {
    user = { calendar_name = "Acc Imported" }
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkCalendarsGone(t, "Acc Imported"),
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["domain"] != "local_calendar" ||
						states[0].Attributes["title"] != "Acc Imported" {
						t.Errorf("imported state = %+v", states)
					}
					return nil
				},
			},
			{
				// The steps cannot be read back, so writing them after the import only records them.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "id", id),
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

func TestAccIntegration_GenerateConfig(t *testing.T) {
	acctest.SharedInstance(t)
	const addr = "homeassistant_integration.generated"
	id := calendarOutsideTofu(t, "Acc Generated")
	// What `tofu plan -generate-config-out` writes after the import.
	generated := acctest.ProviderConfig + `
resource "homeassistant_integration" "generated" {
  domain          = "local_calendar"
  sensitive_steps = null # sensitive
  steps           = null
}
`
	importBlock := fmt.Sprintf(`
import {
  to = %s
  id = %q
}
`, addr, id)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// `tofu plan -generate-config-out` with an import block: the generated configuration
				// leaves the steps null, and plans the import without changes.
				Config:          acctest.ProviderConfig,
				ResourceName:    addr,
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   id,
				GenerateConfig:  true,
				ImportPlanChecks: resource.ImportPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("domain"), knownvalue.StringExact("local_calendar")),
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("steps"), knownvalue.Null()),
					},
				},
			},
			{
				// Applying the import with the configuration as generated changes nothing.
				Config: generated + importBlock,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionNoop)},
				},
				Check: resource.TestCheckResourceAttr(addr, "id", id),
			},
			{
				Config: generated,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A replacement is a create, which needs the steps.
				Config:      strings.Replace(generated, `"local_calendar"`, `"local_todo"`, 1),
				ExpectError: regexp.MustCompile(`Missing steps`),
			},
			{
				// Written later, the steps are only recorded.
				Config: strings.Replace(generated, "steps           = null",
					`steps           = { user = { calendar_name = "Acc Generated" } }`, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "id", id),
			},
		},
	})
}

// An entry imported and given `steps = { user = {} }` with `ignore_changes`, as states written by
// 1.0.0 have it, keeps planning no changes, also once the steps are gone from the configuration.
// Removing them without `ignore_changes` only forgets them.
func TestAccIntegration_StepsAfterImport(t *testing.T) {
	acctest.SharedInstance(t)
	const addr = "homeassistant_integration.kept"
	id := calendarOutsideTofu(t, "Acc Kept")
	config := func(body string) string {
		return acctest.ProviderConfig + `
resource "homeassistant_integration" "kept" {
  domain = "local_calendar"
` + body + `
}
`
	}
	const ignore = "\n  lifecycle { ignore_changes = [steps] }"
	empty := config(`  steps  = { user = {} }`)
	sameID := resource.TestCheckResourceAttr(addr, "id", id)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkCalendarsGone(t, "Acc Kept"),
		Steps: []resource.TestStep{
			{
				Config:             empty,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
			},
			{
				Config: empty,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("steps"),
						knownvalue.ObjectExact(map[string]knownvalue.Check{"user": knownvalue.ObjectExact(nil)})),
				},
			},
			{
				Config: config(`  steps  = { user = {} }` + ignore),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config(ignore),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("steps"), knownvalue.Null()),
				},
				Check: sameID,
			},
			{
				Config: config(`  steps  = { user = { calendar_name = "Acc Kept" } }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: sameID,
			},
		},
	})
}

// A form may be answered from `sensitive_steps` alone (ADR-0021): such an entry is created without
// `steps`, and a changed secret replaces it.
func TestAccIntegration_SecretsOnly(t *testing.T) {
	const addr = "homeassistant_integration.secret"
	config := func(name string) string {
		return acctest.ProviderConfig + `
resource "homeassistant_integration" "secret" {
  domain          = "local_calendar"
  sensitive_steps = { user = { calendar_name = "` + name + `" } }
}
`
	}
	var firstID string
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkCalendarsGone(t, "Acc Secret", "Acc Secret 2"),
		Steps: []resource.TestStep{
			{
				Config: config("Acc Secret"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "title", "Acc Secret"),
					resource.TestCheckResourceAttrWith(addr, "id", func(id string) error { firstID = id; return nil }),
				),
			},
			{
				Config: config("Acc Secret"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: config("Acc Secret 2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "title", "Acc Secret 2"),
					checkCalendarsGone(t, "Acc Secret"),
					resource.TestCheckResourceAttrWith(addr, "id", func(id string) error {
						if id == firstID {
							t.Errorf("entry_id %s did not change", id)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccIntegration_FlowErrors(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// HA aborts the second flow for the same calendar.
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration" "first" {
  domain = "local_calendar"
  steps  = { user = { calendar_name = "Acc Twice" } }
}

resource "homeassistant_integration" "second" {
  domain     = "local_calendar"
  steps      = { user = { calendar_name = "Acc Twice" } }
  depends_on = [homeassistant_integration.first]
}
`,
				ExpectError: regexp.MustCompile(`config flow for local_calendar aborted:\s+already_configured`),
			},
			{
				// HA asks for a step the config does not have; the flow is aborted.
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration" "wrong_step" {
  domain = "local_calendar"
  steps  = { confirm = {} }
}
`,
				ExpectError: regexp.MustCompile(`asked for step "user", but the config only has\s+"confirm"`),
			},
			{
				Config: acctest.ProviderConfig,
				Check:  checkCalendarsGone(t, "Acc Twice"),
			},
		},
	})
}
