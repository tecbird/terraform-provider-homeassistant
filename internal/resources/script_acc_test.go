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

func scripts(t *testing.T) client.ConfigItems {
	t.Helper()
	return client.NewScripts(haClient(t).REST)
}

// checkScriptConfig fails unless HA's stored config of the script means the same as want.
func checkScriptConfig(t *testing.T, id, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := scripts(t).Get(context.Background(), id)
		if err != nil {
			return err
		}
		eq, err := dyntype.SemanticallyEqualJSON(got, []byte(want))
		if err != nil {
			return err
		}
		if !eq {
			return fmt.Errorf("script %q config = %s, want %s", id, got, want)
		}
		return nil
	}
}

func checkScriptGone(t *testing.T, id string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if _, err := scripts(t).Get(context.Background(), id); !errors.Is(err, client.ErrNotFound) {
			return fmt.Errorf("script %q: err = %w; want it gone", id, err)
		}
		return nil
	}
}

const accScript = `
resource "homeassistant_script" "test" {
  id = "acc_goodnight"
  config = {
    alias = %q
    mode  = "single"
    icon  = "mdi:weather-night"
    fields = {
      message = {
        name     = "Message"
        required = true
        selector = { text = {} }
      }
    }
    sequence = [
      {
        action = "persistent_notification.create"
        data   = { title = "Acc", message = "{{ message }}" }
      },
      { delay = { seconds = 1.5 } },
    ]
  }
}
`

const accScriptStored = `{"alias":%q,"mode":"single","icon":"mdi:weather-night",
	"fields":{"message":{"name":"Message","required":true,"selector":{"text":{}}}},
	"sequence":[
		{"action":"persistent_notification.create","data":{"title":"Acc","message":"{{ message }}"}},
		{"delay":{"seconds":1.5}}]}`

func TestAccScript(t *testing.T) {
	const addr = "homeassistant_script.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkScriptGone(t, "acc_goodnight"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(accScript, "Acc Goodnight"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc_goodnight"),
					resource.TestCheckResourceAttr(addr, "entity_id", "script.acc_goodnight"),
					checkScriptConfig(t, "acc_goodnight", fmt.Sprintf(accScriptStored, "Acc Goodnight")),
				),
			},
			{
				// After a refresh, the plan is still empty.
				Config: acctest.ProviderConfig + fmt.Sprintf(accScript, "Acc Goodnight"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc_goodnight",
				ImportStateVerify: true,
			},
			{
				// A new alias updates in place and keeps the entity ID.
				Config: acctest.ProviderConfig + fmt.Sprintf(accScript, "Acc Goodnight Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "entity_id", "script.acc_goodnight"),
					checkScriptConfig(t, "acc_goodnight", fmt.Sprintf(accScriptStored, "Acc Goodnight Renamed")),
				),
			},
			{
				// A change saved in the script editor shows up as drift, and the apply puts the
				// config back.
				PreConfig: func() {
					if err := scripts(t).Save(context.Background(), "acc_goodnight",
						json.RawMessage(`{"alias":"Edited in the UI","sequence":[]}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accScript, "Acc Goodnight Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkScriptConfig(t, "acc_goodnight", fmt.Sprintf(accScriptStored, "Acc Goodnight Renamed")),
			},
			{
				// Deleted outside Tofu: the script drops out of state and is created again.
				PreConfig: func() {
					if err := scripts(t).Delete(context.Background(), "acc_goodnight"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + fmt.Sprintf(accScript, "Acc Goodnight Renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "entity_id", "script.acc_goodnight"),
			},
		},
	})
}

// accScriptYAML is a script as the script editor's "Edit in YAML" shows it.
const accScriptYAML = `alias: Acc Leaving
sequence:
  - action: persistent_notification.create
    metadata: {}
    data:
      title: Leaving
      message: "Left at {{ now().strftime('%H:%M') }}"
  - delay: 2.5
mode: queued
max: 3
description: ""
`

func TestAccScript_YAMLRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "leaving.yaml")
	if err := os.WriteFile(file, []byte(accScriptYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	config := acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_script" "test" {
  id     = "acc_leaving"
  config = yamldecode(file(%q))
}
`, file)
	const addr = "homeassistant_script.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkScriptGone(t, "acc_leaving"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr(addr, "entity_id", "script.acc_leaving"),
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

func TestAccScript_InvalidConfig(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkScriptGone(t, "acc_invalid_script"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_script" "test" {
  id = "acc_invalid_script"
  config = {
    alias    = "Acc Invalid"
    sequence = [{ no_such_step = true }]
  }
}
`,
				ExpectError: regexp.MustCompile(`HTTP 400`),
			},
		},
	})
}

// TestAccScript_LegacyKeys checks the stored baseline (ADR-0023): HA renames `service` to
// `action` on save, and the plan stays empty.
func TestAccScript_LegacyKeys(t *testing.T) {
	const addr = "homeassistant_script.test"
	config := acctest.ProviderConfig + `
resource "homeassistant_script" "test" {
  id = "acc_legacy"
  config = {
    alias    = "Acc Legacy"
    sequence = [{ service = "persistent_notification.create", data = { message = "Legacy" } }]
  }
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkScriptGone(t, "acc_legacy"),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					checkScriptConfig(t, "acc_legacy", `{"alias":"Acc Legacy",
						"sequence":[{"action":"persistent_notification.create","data":{"message":"Legacy"}}]}`),
					resource.TestCheckResourceAttr(addr, "config.sequence.0.service", "persistent_notification.create"),
				),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A change saved in the script editor is still drift.
				PreConfig: func() {
					if err := scripts(t).Save(context.Background(), "acc_legacy",
						json.RawMessage(`{"alias":"Edited in the UI","sequence":[]}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
			},
		},
	})
}

func TestAccScript_upgrade(t *testing.T) {
	const addr = "homeassistant_script.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+fmt.Sprintf(accScript, "Acc Goodnight"),
		resource.TestCheckResourceAttr(addr, "id", "acc_goodnight"),
		checkScriptConfig(t, "acc_goodnight", fmt.Sprintf(accScriptStored, "Acc Goodnight")),
	)
}
