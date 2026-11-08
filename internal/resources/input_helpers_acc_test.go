package resources_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// helpers returns the collection of a helper domain on the shared instance.
func helpers(t *testing.T, domain string) client.Registry[client.Helper] {
	t.Helper()
	return client.Helpers(haClient(t).WS, domain)
}

// checkHelper calls check with the helper as HA stores it, and fails if it is missing.
func checkHelper(t *testing.T, domain, id string, check func(client.Helper) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		h, ok, err := helpers(t, domain).Get(context.Background(), id)
		if err != nil || !ok {
			return fmt.Errorf("%s %q: found %v, err %w", domain, id, ok, err)
		}
		return check(h)
	}
}

// checkHelperGone fails if HA has a helper of the domain with the given ID.
func checkHelperGone(t *testing.T, domain, id string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if _, ok, err := helpers(t, domain).Get(context.Background(), id); err != nil || ok {
			return fmt.Errorf("%s %q: exists = %v, err = %w; want it gone", domain, id, ok, err)
		}
		return nil
	}
}

// importStep imports the resource at addr by id and compares it with the state.
func importStep(addr, id string) resource.TestStep {
	return resource.TestStep{ResourceName: addr, ImportState: true, ImportStateId: id, ImportStateVerify: true}
}

func TestAccInputNumber(t *testing.T) {
	const addr = "homeassistant_input_number.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "input_number", "acc_number"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_number" "test" {
  name    = "Acc Number"
  min     = 0
  max     = 0.5
  step    = 0.4
  initial = 5
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`initial must be between min \(0\) and max \(0.5\), got 5`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_number" "test" {
  name                = "Acc Number"
  icon                = "mdi:thermometer"
  min                 = -10
  max                 = 30.5
  step                = 0.5
  initial             = 21
  mode                = "box"
  unit_of_measurement = "°C"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_number"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_number.acc_number"),
					resource.TestCheckResourceAttr(addr, "min", "-10"),
					resource.TestCheckResourceAttr(addr, "max", "30.5"),
					resource.TestCheckResourceAttr(addr, "step", "0.5"),
					resource.TestCheckResourceAttr(addr, "initial", "21"),
					resource.TestCheckResourceAttr(addr, "mode", "box"),
					resource.TestCheckResourceAttr(addr, "unit_of_measurement", "°C"),
					checkHelper(t, "input_number", "acc_number", func(h client.Helper) error {
						if h["max"] != 30.5 || h["step"] != 0.5 || h["mode"] != "box" {
							return fmt.Errorf("input_number in HA = %v", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_number"),
			{
				// Dropping the optional attributes clears them, or resets them to HA's defaults.
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_number" "test" {
  name = "Acc Number"
  min  = 1
  max  = 5
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "step", "1"),
					resource.TestCheckResourceAttr(addr, "mode", "slider"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					resource.TestCheckNoResourceAttr(addr, "initial"),
					resource.TestCheckNoResourceAttr(addr, "unit_of_measurement"),
					checkHelper(t, "input_number", "acc_number", func(h client.Helper) error {
						if h["initial"] != nil || h["unit_of_measurement"] != nil || h["min"] != 1.0 {
							return fmt.Errorf("input_number in HA = %v; want initial and unit cleared, min 1", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_number"),
		},
	})
}

func TestAccInputText(t *testing.T) {
	const addr = "homeassistant_input_text.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "input_text", "acc_text"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_text" "test" {
  name    = "Acc Text"
  pattern = "[a-z"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`pattern is not a valid regular expression`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_text" "test" {
  name    = "Acc Text"
  min     = 2
  max     = 255
  initial = "hello"
  pattern = "^(?=.*[a-z])\\w+$"
  mode    = "password"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_text"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_text.acc_text"),
					resource.TestCheckResourceAttr(addr, "min", "2"),
					resource.TestCheckResourceAttr(addr, "max", "255"),
					resource.TestCheckResourceAttr(addr, "initial", "hello"),
					resource.TestCheckResourceAttr(addr, "pattern", `^(?=.*[a-z])\w+$`),
					resource.TestCheckResourceAttr(addr, "mode", "password"),
				),
			},
			importStep(addr, "acc_text"),
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_text" "test" {
  name = "Acc Text Renamed"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_text"),
					resource.TestCheckResourceAttr(addr, "min", "0"),
					resource.TestCheckResourceAttr(addr, "max", "100"),
					resource.TestCheckResourceAttr(addr, "mode", "text"),
					resource.TestCheckNoResourceAttr(addr, "initial"),
					resource.TestCheckNoResourceAttr(addr, "pattern"),
					checkHelper(t, "input_text", "acc_text", func(h client.Helper) error {
						if h["pattern"] != nil || h["initial"] != nil || h["name"] != "Acc Text Renamed" {
							return fmt.Errorf("input_text in HA = %v; want pattern and initial cleared", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_text"),
		},
	})
}

func TestAccInputSelect(t *testing.T) {
	const addr = "homeassistant_input_select.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "input_select", "acc_select"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_select" "test" {
  name    = "Acc Select"
  options = ["off", "eco"]
  initial = "comfort"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`initial must be one of options, got "comfort"`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_select" "test" {
  name    = "Acc Select"
  icon    = "mdi:thermostat"
  options = ["off", "eco", "comfort"]
  initial = "eco"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_select"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_select.acc_select"),
					resource.TestCheckResourceAttr(addr, "options.#", "3"),
					resource.TestCheckResourceAttr(addr, "options.2", "comfort"),
					resource.TestCheckResourceAttr(addr, "initial", "eco"),
				),
			},
			importStep(addr, "acc_select"),
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_select" "test" {
  name    = "Acc Select"
  options = ["comfort", "off"]
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "options.#", "2"),
					resource.TestCheckResourceAttr(addr, "options.0", "comfort"),
					resource.TestCheckNoResourceAttr(addr, "initial"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
					checkHelper(t, "input_select", "acc_select", func(h client.Helper) error {
						if fmt.Sprint(h["options"]) != "[comfort off]" || h["initial"] != nil {
							return fmt.Errorf("input_select in HA = %v; want options [comfort off], initial cleared", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_select"),
		},
	})
}

func TestAccInputDatetime(t *testing.T) {
	const addr = "homeassistant_input_datetime.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "input_datetime", "acc_datetime"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_datetime" "test" {
  name = "Acc Datetime"
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`At least one of has_date and has_time must be true`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_datetime" "test" {
  name     = "Acc Datetime"
  has_date = true
  has_time = true
  initial  = "2026-12-24 18:30:00"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_datetime"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_datetime.acc_datetime"),
					resource.TestCheckResourceAttr(addr, "has_date", "true"),
					resource.TestCheckResourceAttr(addr, "has_time", "true"),
					resource.TestCheckResourceAttr(addr, "initial", "2026-12-24 18:30:00"),
				),
			},
			importStep(addr, "acc_datetime"),
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_datetime" "test" {
  name     = "Acc Datetime"
  has_time = true
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "has_date", "false"),
					resource.TestCheckResourceAttr(addr, "has_time", "true"),
					resource.TestCheckNoResourceAttr(addr, "initial"),
					checkHelper(t, "input_datetime", "acc_datetime", func(h client.Helper) error {
						if h["has_date"] != false || h["initial"] != nil {
							return fmt.Errorf("input_datetime in HA = %v; want has_date false, initial cleared", h)
						}
						return nil
					}),
				),
			},
			importStep(addr, "acc_datetime"),
		},
	})
}

func TestAccInputButton(t *testing.T) {
	const addr = "homeassistant_input_button.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkHelperGone(t, "input_button", "acc_button"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_button" "test" {
  name = "Acc Button"
  icon = "mdi:bell"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_button"),
					resource.TestCheckResourceAttr(addr, "entity_id", "input_button.acc_button"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:bell"),
				),
			},
			importStep(addr, "acc_button"),
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_input_button" "test" {
  name = "Acc Button Renamed"
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_button"),
					resource.TestCheckResourceAttr(addr, "name", "Acc Button Renamed"),
					resource.TestCheckNoResourceAttr(addr, "icon"),
				),
			},
			importStep(addr, "acc_button"),
		},
	})
}

func TestAccInputNumber_upgrade(t *testing.T) {
	const addr = "homeassistant_input_number.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_number" "test" {
  name                = "Acc Number"
  icon                = "mdi:thermometer"
  min                 = -10
  max                 = 30.5
  step                = 0.5
  initial             = 21
  mode                = "box"
  unit_of_measurement = "°C"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_number"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_number.acc_number"),
		resource.TestCheckResourceAttr(addr, "max", "30.5"),
	)
}

func TestAccInputText_upgrade(t *testing.T) {
	const addr = "homeassistant_input_text.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_text" "test" {
  name    = "Acc Text"
  min     = 2
  max     = 255
  initial = "hello"
  pattern = "^(?=.*[a-z])\\w+$"
  mode    = "password"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_text"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_text.acc_text"),
		resource.TestCheckResourceAttr(addr, "pattern", `^(?=.*[a-z])\w+$`),
	)
}

func TestAccInputSelect_upgrade(t *testing.T) {
	const addr = "homeassistant_input_select.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_select" "test" {
  name    = "Acc Select"
  icon    = "mdi:thermostat"
  options = ["off", "eco", "comfort"]
  initial = "eco"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_select"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_select.acc_select"),
		resource.TestCheckResourceAttr(addr, "options.#", "3"),
	)
}

func TestAccInputDatetime_upgrade(t *testing.T) {
	const addr = "homeassistant_input_datetime.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_datetime" "test" {
  name     = "Acc Datetime"
  has_date = true
  has_time = true
  initial  = "2026-12-24 18:30:00"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_datetime"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_datetime.acc_datetime"),
		resource.TestCheckResourceAttr(addr, "initial", "2026-12-24 18:30:00"),
	)
}

func TestAccInputButton_upgrade(t *testing.T) {
	const addr = "homeassistant_input_button.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+`
resource "homeassistant_input_button" "test" {
  name = "Acc Button"
  icon = "mdi:bell"
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc_button"),
		resource.TestCheckResourceAttr(addr, "entity_id", "input_button.acc_button"),
		resource.TestCheckResourceAttr(addr, "icon", "mdi:bell"),
	)
}
