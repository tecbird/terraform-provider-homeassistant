package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// The tests use threshold: its config flow and its options flow need no hardware, and its
// options form shows the current options as suggested values.

// thresholdEntry creates a threshold entry outside Tofu, as the UI would, and deletes it when the
// test ends.
func thresholdEntry(t *testing.T, name string) string {
	t.Helper()
	ctx := context.Background()
	id, err := client.NewConfigFlows(haClient(t).REST).Run(ctx, "threshold", "", []client.FlowStep{{
		StepID: "user",
		Data:   map[string]any{"name": name, "entity_id": "sensor.acc_source", "upper": 10, "hysteresis": 0},
	}})
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

// thresholdOptions returns the current options of the entry, as its options form shows them.
func thresholdOptions(t *testing.T, id string) string {
	t.Helper()
	form, err := client.NewOptionsFlows(haClient(t).REST).Form(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(client.CurrentValues(form.Fields))
	return string(js)
}

// checkThresholdOptions fails unless the entry's options are want.
func checkThresholdOptions(t *testing.T, id, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := thresholdOptions(t, id); got != want {
			t.Errorf("options = %s\nwant      %s", got, want)
		}
		return nil
	}
}

func TestAccIntegrationOptions(t *testing.T) {
	acctest.SharedInstance(t)
	const addr = "homeassistant_integration_options.threshold"
	id := thresholdEntry(t, "Acc Threshold")
	config := func(steps string) string {
		return acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_integration_options" "threshold" {
  entry_id = %q
  steps    = %s
}
`, id, steps)
	}
	// What `tofu plan -generate-config-out` writes after the import.
	imported := config(`{ init = { entity_id = "sensor.acc_source", hysteresis = 0, upper = 10 } }`)
	upper20 := config(`{ init = { upper = 20 } }`)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		// Destroy leaves the options as they are.
		CheckDestroy: checkThresholdOptions(t, id, `{"entity_id":"sensor.acc_source","hysteresis":1.0,"upper":20.0}`),
		Steps: []resource.TestStep{
			{
				Config:             imported,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["entry_id"] != id ||
						states[0].Attributes["domain"] != "threshold" {
						t.Errorf("imported state = %+v", states)
					}
					return nil
				},
			},
			{
				// The import read the options, so the generated configuration has nothing to do.
				Config: imported,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Only upper is managed from here on; the other options keep their values.
				Config: upper20,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkThresholdOptions(t, id, `{"entity_id":"sensor.acc_source","hysteresis":0.0,"upper":20.0}`),
			},
			{
				Config: upper20,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Changed outside Tofu: drift, and the apply sets it back.
				PreConfig: func() {
					err := client.NewOptionsFlows(haClient(t).REST).Set(context.Background(), id,
						map[string]map[string]any{"init": {"upper": 30, "hysteresis": 1}})
					if err != nil {
						t.Fatal(err)
					}
				},
				Config: upper20,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				// hysteresis is not managed, so its change outside Tofu stays.
				Check: checkThresholdOptions(t, id, `{"entity_id":"sensor.acc_source","hysteresis":1.0,"upper":20.0}`),
			},
			{
				Config: config(`{ init = { upper = 20, lower = 5 } }`),
				Check:  checkThresholdOptions(t, id, `{"entity_id":"sensor.acc_source","hysteresis":1.0,"lower":5.0,"upper":20.0}`),
			},
			{
				// null unsets an optional field.
				Config: config(`{ init = { upper = 20, lower = null } }`),
				Check:  checkThresholdOptions(t, id, `{"entity_id":"sensor.acc_source","hysteresis":1.0,"upper":20.0}`),
			},
			{
				Config: config(`{ init = { upper = 20, lower = null } }`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config:      config(`{ init = { uper = 20 } }`),
				ExpectError: regexp.MustCompile(`has no\s+field "uper"`),
			},
		},
	})
}

func TestAccIntegrationOptions_NoOptionsFlow(t *testing.T) {
	acctest.SharedInstance(t)
	ctx := context.Background()
	id, err := client.NewConfigFlows(haClient(t).REST).Run(ctx, "local_calendar", "", []client.FlowStep{
		{StepID: "user", Data: map[string]any{"calendar_name": "Acc No Options"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := configEntries(t).Delete(ctx, id); err != nil {
			t.Error(err)
		}
	})

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_integration_options" "calendar" {
  entry_id = %q
  steps    = { init = {} }
}
`, id),
				ExpectError: regexp.MustCompile(`has no options flow`),
			},
		},
	})
}
