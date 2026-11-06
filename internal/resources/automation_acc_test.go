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

func automations(t *testing.T) client.ConfigItems {
	t.Helper()
	return client.NewAutomations(haClient(t).REST)
}

// checkAutomationConfig fails unless HA's stored config of the automation, without its id, means
// the same as want.
func checkAutomationConfig(t *testing.T, id, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := automations(t).Get(context.Background(), id)
		if err != nil {
			return err
		}
		eq, err := dyntype.SemanticallyEqualJSON(got, []byte(want))
		if err != nil {
			return err
		}
		if !eq {
			return fmt.Errorf("automation %q config = %s, want %s", id, got, want)
		}
		return nil
	}
}

// checkAutomationAlias fails unless HA's stored config of the automation has the given alias.
func checkAutomationAlias(t *testing.T, id, alias string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		raw, err := automations(t).Get(context.Background(), id)
		if err != nil {
			return err
		}
		var config struct {
			Alias string `json:"alias"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return err
		}
		if config.Alias != alias {
			return fmt.Errorf("automation %q has alias %q, want %q", id, config.Alias, alias)
		}
		return nil
	}
}

func checkAutomationGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, err := automations(t).Get(context.Background(), id); !errors.Is(err, client.ErrNotFound) {
			return fmt.Errorf("automation %q: err = %w; want it gone", id, err)
		}
		return nil
	}
}

const accAutomation = `
locals {
  notify = {
    action = "persistent_notification.create"
    data   = { title = "Acc", message = "Motion" }
  }
}

resource "homeassistant_automation" "test" {
  id = "acc_motion"
  config = {
    alias = %q
    mode  = "restart"
    triggers = [{
      trigger   = "state"
      entity_id = "sun.sun"
      to        = "below_horizon"
    }]
    conditions = [{
      condition = "time"
      after     = "22:00:00"
      before    = "08:00:00"
    }]
    actions = [local.notify, { delay = { seconds = 1.5 } }]
  }
}
`

func TestAccAutomation(t *testing.T) {
	const addr = "homeassistant_automation.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkAutomationGone(t, "acc_motion"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(accAutomation, "Acc Motion"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_motion"),
					resource.TestCheckResourceAttr(addr, "entity_id", "automation.acc_motion"),
					checkAutomationConfig(t, "acc_motion", `{"alias":"Acc Motion","mode":"restart",
						"triggers":[{"trigger":"state","entity_id":"sun.sun","to":"below_horizon"}],
						"conditions":[{"condition":"time","after":"22:00:00","before":"08:00:00"}],
						"actions":[
							{"action":"persistent_notification.create","data":{"title":"Acc","message":"Motion"}},
							{"delay":{"seconds":1.5}}]}`),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_motion",
				ImportStateVerify: true,
			},
			{
				// A new alias updates in place and keeps the entity ID.
				Config: acctest.ProviderConfig + fmt.Sprintf(accAutomation, "Acc Motion Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "entity_id", "automation.acc_motion"),
					checkAutomationAlias(t, "acc_motion", "Acc Motion Renamed"),
				),
			},
			{
				// A change saved in the automation editor shows up as drift, and the apply puts
				// the config back.
				PreConfig: func() {
					if err := automations(t).Save(context.Background(), "acc_motion",
						json.RawMessage(`{"alias":"Edited in the UI","triggers":[],"actions":[]}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accAutomation, "Acc Motion Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkAutomationConfig(t, "acc_motion", `{"alias":"Acc Motion Renamed","mode":"restart",
					"triggers":[{"trigger":"state","entity_id":"sun.sun","to":"below_horizon"}],
					"conditions":[{"condition":"time","after":"22:00:00","before":"08:00:00"}],
					"actions":[
						{"action":"persistent_notification.create","data":{"title":"Acc","message":"Motion"}},
						{"delay":{"seconds":1.5}}]}`),
			},
			{
				// Deleted outside Tofu: the automation drops out of state and is created again.
				PreConfig: func() {
					if err := automations(t).Delete(context.Background(), "acc_motion"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accAutomation, "Acc Motion Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.TestCheckResourceAttrSet(addr, "entity_id"),
			},
		},
	})
}

// accAutomationYAML is an automation as the automation editor's "Edit in YAML" shows it, minus
// its id line.
const accAutomationYAML = `alias: Acc Away
description: ""
triggers:
  - trigger: state
    entity_id: sun.sun
    to: below_horizon
    for:
      minutes: 5
conditions: []
actions:
  - action: persistent_notification.create
    metadata: {}
    data:
      title: Away
      message: "Sun set at {{ now().strftime('%H:%M') }}"
  - delay: 2.5
mode: single
initial_state: false
`

func TestAccAutomation_YAMLRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "away.yaml")
	if err := os.WriteFile(file, []byte(accAutomationYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	config := acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_automation" "test" {
  id       = "acc_away"
  config   = yamldecode(file(%q))
  timeouts = { create = "2m" }
}
`, file)
	const addr = "homeassistant_automation.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkAutomationGone(t, "acc_away"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr(addr, "entity_id", "automation.acc_away"),
			},
			{
				// After a refresh, the plan is still empty.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccAutomation_InvalidConfig(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkAutomationGone(t, "acc_invalid"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_automation" "test" {
  id = "acc_invalid"
  config = {
    alias    = "Acc Invalid"
    triggers = [{ trigger = "no_such_trigger" }]
    actions  = []
  }
}
`,
				ExpectError: regexp.MustCompile(`(?s)HTTP 400.*no_such_trigger`),
			},
		},
	})
}

// An automation that already exists with the ID, e.g. made in the editor, is not overwritten
// (ADR-0024).
func TestAccAutomation_IDCollision(t *testing.T) {
	const existing = `{"alias":"Made in the UI","triggers":[],"actions":[]}`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				PreConfig: func() {
					if err := automations(t).Save(context.Background(), "acc_taken", json.RawMessage(existing)); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = automations(t).Delete(context.Background(), "acc_taken") })
				},
				Config: acctest.ProviderConfig + `
resource "homeassistant_automation" "test" {
  id     = "acc_taken"
  config = { alias = "Acc Taken", triggers = [], actions = [] }
}
`,
				ExpectError: regexp.MustCompile(`(?s)already\s+has\s+an\s+automation\s+with\s+the\s+ID\s+"acc_taken".*tofu\s+import`),
			},
			{
				// The existing automation is unchanged.
				Config: acctest.ProviderConfig,
				Check:  checkAutomationConfig(t, "acc_taken", existing),
			},
		},
	})
}

func TestAccAutomation_IDInConfig(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_automation" "test" {
  id     = "acc_id_in_config"
  config = { id = "other", alias = "Acc", triggers = [], actions = [] }
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must not contain\s+an\s+.id.\s+key`),
			},
		},
	})
}

// TestAccAutomation_builderFunctions builds an automation config with the trigger, condition and
// action builder functions, so the arguments pass through OpenTofu's own type conversion.
func TestAccAutomation_builderFunctions(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkAutomationGone(t, "acc_functions"),
		Steps: []resource.TestStep{{
			Config: acctest.ProviderConfig + `
resource "homeassistant_automation" "test" {
  id = "acc_functions"
  config = {
    alias = "Acc Functions"
    triggers = [
      provider::homeassistant::state_trigger(["sun.sun"], { "for" = { minutes = 5 }, to = "below_horizon", from = null }),
      provider::homeassistant::numeric_state_trigger("sun.sun", { attribute = "elevation", below = -6 }),
      provider::homeassistant::time_trigger("07:30:00"),
    ]
    conditions = [
      provider::homeassistant::state_condition("sun.sun", ["below_horizon", "above_horizon"]),
      provider::homeassistant::time_condition({ after = "22:00:00" }),
    ]
    actions = [
      provider::homeassistant::action("persistent_notification.create", { data = { message = "Dusk" } }),
      provider::homeassistant::delay(1.5),
    ]
  }
}
`,
			Check: checkAutomationConfig(t, "acc_functions", `{"alias":"Acc Functions",
				"triggers":[
					{"trigger":"state","entity_id":["sun.sun"],"to":"below_horizon","for":{"minutes":5}},
					{"trigger":"numeric_state","entity_id":"sun.sun","attribute":"elevation","below":-6},
					{"trigger":"time","at":"07:30:00"}],
				"conditions":[
					{"condition":"state","entity_id":"sun.sun","state":["below_horizon","above_horizon"]},
					{"condition":"time","after":"22:00:00"}],
				"actions":[
					{"action":"persistent_notification.create","data":{"message":"Dusk"}},
					{"delay":1.5}]}`),
		}},
	})
}

// accAutomationLegacy uses the old key names, which HA renames when it saves (issue #36).
const accAutomationLegacy = `
resource "homeassistant_automation" "test" {
  id = "acc_legacy"
  config = {
    alias     = "Acc Legacy"
    trigger   = [{ platform = "state", entity_id = ["sun.sun"], to = "below_horizon" }]
    condition = []
    action = [{
      choose = [{
        conditions = [{ condition = "state", entity_id = "sun.sun", state = "below_horizon" }]
        sequence   = [{ service = "persistent_notification.create", data = { message = "Dusk" } }]
      }]
    }]
    mode = "single"
  }
}
`

// TestAccAutomation_LegacyKeys checks the stored baseline (ADR-0023): HA renames old keys on
// save, and the plan stays empty, while a change made outside Tofu is still drift.
func TestAccAutomation_LegacyKeys(t *testing.T) {
	const addr = "homeassistant_automation.test"
	config := acctest.ProviderConfig + accAutomationLegacy
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkAutomationGone(t, "acc_legacy"),
		Steps: []resource.TestStep{
			{
				Config: config,
				// HA has renamed the keys, but state keeps the user's spelling.
				Check: resource.ComposeAggregateTestCheckFunc(
					checkAutomationConfig(t, "acc_legacy", `{"alias":"Acc Legacy","mode":"single",
						"triggers":[{"platform":"state","entity_id":["sun.sun"],"to":"below_horizon"}],
						"conditions":[],
						"actions":[{"choose":[{
							"conditions":[{"condition":"state","entity_id":"sun.sun","state":"below_horizon"}],
							"sequence":[{"action":"persistent_notification.create","data":{"message":"Dusk"}}]}]}]}`),
					resource.TestCheckResourceAttr(addr, "config.action.0.choose.0.sequence.0.service",
						"persistent_notification.create"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A change saved in the automation editor is still drift.
				PreConfig: func() {
					if err := automations(t).Save(context.Background(), "acc_legacy",
						json.RawMessage(`{"alias":"Edited in the UI","triggers":[],"actions":[]}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkAutomationAlias(t, "acc_legacy", "Acc Legacy"),
			},
			{
				// The update recorded a new baseline.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccAutomation_upgrade(t *testing.T) {
	const addr = "homeassistant_automation.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+fmt.Sprintf(accAutomation, "Acc Motion"),
		resource.TestCheckResourceAttr(addr, "id", "acc_motion"),
		resource.TestCheckResourceAttr(addr, "entity_id", "automation.acc_motion"),
	)
}
