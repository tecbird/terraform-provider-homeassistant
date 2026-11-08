package resources_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// The tests run mosquitto in HA's network namespace (acctest.StartMosquitto), so the broker is
// `localhost`. MQTT allows one config entry, so the tests must not run in parallel.

// formField is a field of a form's data_schema, with what HA pre-fills.
type formField struct {
	Name        string          `json:"name"`
	Default     json.RawMessage `json:"default"`
	Description struct {
		SuggestedValue json.RawMessage `json:"suggested_value"`
	} `json:"description"`
	Schema []formField `json:"schema"`
}

// formValues starts a flow at path with init, returns what its first form pre-fills (defaults and
// suggested values, also from sections), and aborts the flow. The data and options of an entry
// cannot be read, but its reconfigure and options forms show them.
func formValues(t *testing.T, path string, init map[string]any) map[string]string {
	t.Helper()
	rest := haClient(t).REST
	var res struct {
		FlowID     string      `json:"flow_id"`
		StepID     string      `json:"step_id"`
		DataSchema []formField `json:"data_schema"`
	}
	ctx := context.Background()
	if err := rest.Do(ctx, http.MethodPost, path, init, &res); err != nil {
		t.Fatal(err)
	}
	if err := rest.Do(ctx, http.MethodDelete, path+"/"+res.FlowID, nil, nil); err != nil {
		t.Error(err)
	}
	values := map[string]string{}
	var collect func([]formField)
	collect = func(fields []formField) {
		for _, f := range fields {
			collect(f.Schema)
			switch {
			case len(f.Description.SuggestedValue) > 0:
				values[f.Name] = string(f.Description.SuggestedValue)
			case len(f.Default) > 0:
				values[f.Name] = string(f.Default)
			}
		}
	}
	collect(res.DataSchema)
	return values
}

// checkForm checks that the form at path pre-fills each field with the JSON in want.
func checkForm(t *testing.T, addr, path string, init func(entryID string) map[string]any, want map[string]string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		got := formValues(t, path, init(s.RootModule().Resources[addr].Primary.ID))
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %s, want %s (form: %v)", path, k, got[k], v, got)
			}
		}
		return nil
	}
}

func mqttOptionsFlow(entryID string) map[string]any {
	return map[string]any{"handler": entryID}
}

func mqttReconfigureFlow(entryID string) map[string]any {
	return map[string]any{"handler": "mqtt", "entry_id": entryID, "show_advanced_options": true}
}

// checkMQTTGone fails if HA has an MQTT config entry.
func checkMQTTGone(t *testing.T) resource.TestCheckFunc {
	return func(*terraform.State) error {
		entries, err := configEntries(t).List(context.Background(), "mqtt")
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			t.Errorf("MQTT entries still exist: %+v", entries)
		}
		return nil
	}
}

func mqttConfig(extra string) string {
	return acctest.ProviderConfig + `
resource "homeassistant_mqtt" "test" {
  broker   = "localhost"
  password = "unused"
` + extra + `
}
`
}

func TestAccMQTT(t *testing.T) {
	const addr = "homeassistant_mqtt.test"
	var firstID string
	first := mqttConfig(`
  discovery_prefix = "acc"
  birth_message    = { topic = "acc/status" }
`)
	reconfigured := mqttConfig(`
  keepalive        = 30
  discovery_prefix = "acc2"
  birth_message    = { topic = "acc/status" }
`)
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			acctest.PreCheck(t)
			acctest.StartMosquitto(t)
		},
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkMQTTGone(t),
		Steps: []resource.TestStep{
			{
				Config: first,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectSensitiveValue(addr, tfjsonpath.New("password"))},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "title", "localhost"),
					resource.TestCheckResourceAttrSet(addr, "state"),
					resource.TestCheckResourceAttr(addr, "port", "1883"),
					resource.TestCheckResourceAttr(addr, "birth_message.payload", "online"),
					resource.TestCheckResourceAttrWith(addr, "id", func(id string) error { firstID = id; return nil }),
					checkForm(t, addr, "config/config_entries/options/flow", mqttOptionsFlow, map[string]string{
						"discovery_prefix": `"acc"`, "birth_topic": `"acc/status"`, "birth_payload": `"online"`,
						"will_topic": `"homeassistant/status"`, "will_payload": `"offline"`,
					}),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"state", "broker", "port", "username", "password", "client_id", "keepalive", "protocol", "transport",
					"discovery", "discovery_prefix", "birth_message.%", "birth_message.enabled", "birth_message.topic",
					"birth_message.payload", "birth_message.qos", "birth_message.retain", "will_message.%",
					"will_message.enabled", "will_message.topic", "will_message.payload", "will_message.qos",
					"will_message.retain",
				},
			},
			{
				// Same inputs: nothing to do, although HA cannot return them.
				Config: first,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A broker setting and an option change in place: a reconfigure and an options flow.
				Config: reconfigured,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrWith(addr, "id", func(id string) error {
						if id != firstID {
							t.Errorf("entry_id changed from %s to %s", firstID, id)
						}
						return nil
					}),
					checkForm(t, addr, "config/config_entries/flow", mqttReconfigureFlow, map[string]string{
						"broker": `"localhost"`, "keepalive": "30",
					}),
					checkForm(t, addr, "config/config_entries/options/flow", mqttOptionsFlow, map[string]string{
						"discovery_prefix": `"acc2"`, "birth_topic": `"acc/status"`,
					}),
				),
			},
			{
				// Removing the password replaces the entry: a reconfigure would keep it.
				Config: reconfigured[:len(acctest.ProviderConfig)] + `
resource "homeassistant_mqtt" "test" {
  broker    = "localhost"
  keepalive = 30
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.TestCheckResourceAttrSet(addr, "state"),
			},
		},
	})
}

func TestAccMQTT_Import(t *testing.T) {
	acctest.SharedInstance(t)
	acctest.StartMosquitto(t)
	const addr = "homeassistant_mqtt.imported"
	ctx := context.Background()
	rest := haClient(t).REST
	id, err := client.NewConfigFlows(rest).RunAnswer(ctx, "mqtt", "", client.FieldAnswer(rest, map[string]any{
		"broker": "localhost", "set_ca_cert": "off", "set_client_cert": false,
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := configEntries(t).Delete(ctx, id); err != nil {
			t.Error(err)
		}
	})

	config := acctest.ProviderConfig + `
resource "homeassistant_mqtt" "imported" {
  broker           = "localhost"
  discovery_prefix = "not-sent"
}
`
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkMQTTGone(t),
		Steps: []resource.TestStep{
			{
				Config:             config,
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["title"] != "localhost" {
						t.Errorf("imported state = %+v", states)
					}
					return nil
				},
			},
			{
				// The inputs cannot be read back, so writing them after the import only records them.
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", id),
					checkForm(t, addr, "config/config_entries/options/flow", mqttOptionsFlow, map[string]string{
						"discovery_prefix": `"homeassistant"`,
					}),
				),
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

func TestAccMQTT_upgrade(t *testing.T) {
	const addr = "homeassistant_mqtt.test"
	acctest.StartMosquitto(t)
	acctest.UpgradeTest(t, mqttConfig(`
  discovery_prefix = "acc"
  birth_message    = { topic = "acc/status" }
`),
		resource.TestCheckResourceAttr(addr, "broker", "localhost"),
		resource.TestCheckResourceAttr(addr, "discovery_prefix", "acc"),
	)
}
