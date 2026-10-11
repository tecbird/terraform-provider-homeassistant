package resources_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

func TestAccCounter(t *testing.T) {
	const addr = "homeassistant_counter.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "counter", "acc_counter"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_counter" "test" {
  name    = "Acc Counter"
  minimum = 5
  maximum = 1
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`maximum must not be less than minimum, got minimum = 5 and maximum = 1`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_counter" "test" {
  name    = "Acc Counter"
  icon    = "mdi:counter"
  initial = 5
  minimum = 0
  maximum = 10
  step    = 2
  restore = false
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_counter"),
					resource.TestCheckResourceAttr(addr, "entity_id", "counter.acc_counter"),
					resource.TestCheckResourceAttr(addr, "initial", "5"),
					resource.TestCheckResourceAttr(addr, "minimum", "0"),
					resource.TestCheckResourceAttr(addr, "maximum", "10"),
					resource.TestCheckResourceAttr(addr, "step", "2"),
					resource.TestCheckResourceAttr(addr, "restore", "false"),
					checkHelper(t, "counter", "acc_counter", func(h client.Helper) error {
						if h["initial"] != 5.0 || h["minimum"] != 0.0 || h["maximum"] != 10.0 || h["step"] != 2.0 || h["restore"] != false {
							return fmt.Errorf("counter in HA = %v", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_counter"),
			{
				// Dropping the optional attributes clears them, or resets them to HA's defaults.
				Config: acctest.ProviderConfig + `
resource "homeassistant_counter" "test" {
  name = "Acc Counter"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "initial", "0"),
					resource.TestCheckResourceAttr(addr, "step", "1"),
					resource.TestCheckResourceAttr(addr, "restore", "true"),
					resource.TestCheckNoResourceAttr(addr, "minimum"),
					resource.TestCheckNoResourceAttr(addr, "maximum"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					checkHelper(t, "counter", "acc_counter", func(h client.Helper) error {
						if h["minimum"] != nil || h["maximum"] != nil || h["icon"] != nil || h["step"] != 1.0 {
							return fmt.Errorf("counter in HA = %v; want bounds and icon cleared, step 1", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_counter"),
		},
	})
}

func TestAccTimer(t *testing.T) {
	const addr = "homeassistant_timer.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "timer", "acc_timer"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_timer" "test" {
  name     = "Acc Timer"
  duration = "00:05:00"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`duration must have the format H:MM:SS, e.g. 0:05:00, got "00:05:00"`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_timer" "test" {
  name     = "Acc Timer"
  icon     = "mdi:timer"
  duration = "36:00:30"
  restore  = true
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_timer"),
					resource.TestCheckResourceAttr(addr, "entity_id", "timer.acc_timer"),
					resource.TestCheckResourceAttr(addr, "duration", "36:00:30"),
					resource.TestCheckResourceAttr(addr, "restore", "true"),
					checkHelper(t, "timer", "acc_timer", func(h client.Helper) error {
						if h["duration"] != "36:00:30" || h["restore"] != true {
							return fmt.Errorf("timer in HA = %v", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_timer"),
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_timer" "test" {
  name = "Acc Timer"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "duration", "0:00:00"),
					resource.TestCheckResourceAttr(addr, "restore", "false"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					checkHelper(t, "timer", "acc_timer", func(h client.Helper) error {
						if h["duration"] != "0:00:00" || h["restore"] != false || h["icon"] != nil {
							return fmt.Errorf("timer in HA = %v; want the defaults", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_timer"),
		},
	})
}

func TestAccSchedule(t *testing.T) {
	const addr = "homeassistant_schedule.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "schedule", "acc_schedule"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_schedule" "test" {
  name   = "Acc Schedule"
  monday = [
    { from = "07:00:00", to = "12:00:00" },
    { from = "11:00:00", to = "13:00:00" },
  ]
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`the range 11:00:00-13:00:00 overlaps the range 07:00:00-12:00:00`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_schedule" "test" {
  name   = "Acc Schedule"
  friday = [
    { from = "13:00:00", to = "14:00:00" },
    { from = "07:00:00", to = "08:00:00" },
  ]
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must be listed in order of from`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_schedule" "test" {
  name = "Acc Schedule"
  icon = "mdi:calendar-clock"
  monday = [
    { from = "07:00:00", to = "12:00:00" },
    { from = "13:30:00", to = "17:00:00" },
  ]
  sunday = [{ from = "20:00:00", to = "24:00:00" }]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_schedule"),
					resource.TestCheckResourceAttr(addr, "entity_id", "schedule.acc_schedule"),
					resource.TestCheckResourceAttr(addr, "monday.#", "2"),
					resource.TestCheckResourceAttr(addr, "monday.1.from", "13:30:00"),
					resource.TestCheckResourceAttr(addr, "tuesday.#", "0"),
					resource.TestCheckResourceAttr(addr, "sunday.0.to", "24:00:00"),
					checkHelper(t, "schedule", "acc_schedule", func(h client.Helper) error {
						monday, _ := h["monday"].([]any)
						sunday, _ := h["sunday"].([]any)
						tuesday, ok := h["tuesday"].([]any)
						if len(monday) != 2 || len(sunday) != 1 || !ok || len(tuesday) != 0 {
							return fmt.Errorf("schedule in HA = %v", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_schedule"),
			{
				// Dropping a day clears it; the other days stay.
				Config: acctest.ProviderConfig + `
resource "homeassistant_schedule" "test" {
  name   = "Acc Schedule"
  sunday = [{ from = "20:00:00", to = "24:00:00" }]
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "monday.#", "0"),
					resource.TestCheckResourceAttr(addr, "sunday.#", "1"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					checkHelper(t, "schedule", "acc_schedule", func(h client.Helper) error {
						if monday, ok := h["monday"].([]any); !ok || len(monday) != 0 || h["icon"] != nil {
							return fmt.Errorf("schedule in HA = %v; want monday and icon cleared", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_schedule"),
			{
				// The state matches the config after the apply: the next plan is empty.
				Config: acctest.ProviderConfig + `
resource "homeassistant_schedule" "test" {
  name   = "Acc Schedule"
  sunday = [{ from = "20:00:00", to = "24:00:00" }]
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
