package resources_test

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
)

// The tests use forecast_solar: its config flow and its `plane` subentry flow need no hardware and
// do not contact the service, and they work the same in every release of the support window. The
// config flow creates the entry with one plane. A second plane needs an API key in the entry's
// options, so the tests add planes only while the entry has none. A plane has a reconfigure flow.

// checkOnlyPlane checks that the entry has exactly one plane in HA, the one with the ID that id
// returns, titled title.
func checkOnlyPlane(t *testing.T, entryID string, id func() string, title string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		planes := solarPlanes(t, entryID)
		if len(planes) != 1 || planes[0].SubentryID != id() || planes[0].Title != title {
			t.Errorf("planes in HA = %+v, want only %s titled %q", planes, id(), title)
		}
		return nil
	}
}

// configSubentries returns the config-subentry API of the shared instance.
func configSubentries(t *testing.T) client.ConfigSubentries {
	t.Helper()
	c := haClient(t)
	return client.NewConfigSubentries(c.WS, c.REST)
}

// solarEntry creates a forecast_solar config entry, deleted when the test ends, and returns its
// entry_id and the subentry_id of the plane the config flow created with it.
func solarEntry(t *testing.T) (entryID, planeID string) {
	t.Helper()
	ctx := context.Background()
	entryID, err := client.NewConfigFlows(haClient(t).REST).Run(ctx, "forecast_solar", "", []client.FlowStep{{
		StepID: "user",
		Data: map[string]any{
			"latitude": 52.37, "longitude": 9.73, "declination": 25, "azimuth": 180, "modules_power": 10000,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := configEntries(t).Delete(ctx, entryID); err != nil {
			t.Error(err)
		}
	})
	planes := solarPlanes(t, entryID)
	if len(planes) != 1 {
		t.Fatalf("forecast_solar created the planes %+v, want one", planes)
	}
	return entryID, planes[0].SubentryID
}

// solarPlanes returns the subentries of the config entry.
func solarPlanes(t *testing.T, entryID string) []client.ConfigSubentry {
	t.Helper()
	planes, err := configSubentries(t).List(context.Background(), entryID)
	if err != nil {
		t.Fatal(err)
	}
	return planes
}

// planeConfig is a homeassistant_integration_subentry "plane" of the entry. The power is sent as
// sensitive data, which a plane does not need, but which shows the merge.
func planeConfig(entryID string, declination, power int) string {
	return acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_integration_subentry" "plane" {
  entry_id      = %q
  subentry_type = "plane"
  steps = {
    user = { declination = %d, azimuth = 180, modules_power = 1 }
  }
  sensitive_steps = {
    user = { modules_power = %d }
  }
}
`, entryID, declination, power)
}

func TestAccIntegrationSubentry(t *testing.T) {
	acctest.SharedInstance(t)
	const addr = "homeassistant_integration_subentry.plane"
	entryID, autoPlane := solarEntry(t)
	var planeID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			if planes := solarPlanes(t, entryID); len(planes) != 0 {
				t.Errorf("planes left after destroy: %+v", planes)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// A second plane needs an API key: HA aborts, and nothing is created.
				Config:      planeConfig(entryID, 30, 5000),
				ExpectError: regexp.MustCompile(`subentry flow for plane of config entry \S+ aborted:\s+api_key_required`),
			},
			{
				// Import the plane the config flow created.
				PreConfig: func() {
					if planes := solarPlanes(t, entryID); len(planes) != 1 {
						t.Fatalf("planes after the aborted flow = %+v, want only the first", planes)
					}
				},
				Config:             planeConfig(entryID, 25, 10000),
				ResourceName:       addr,
				ImportState:        true,
				ImportStateId:      entryID + "/" + autoPlane,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					want := map[string]string{
						"id": entryID + "/" + autoPlane, "entry_id": entryID, "subentry_id": autoPlane,
						"subentry_type": "plane", "title": "25° / 180° / 10000W", "supports_reconfigure": "true",
					}
					if len(states) != 1 {
						t.Fatalf("imported %d states", len(states))
					}
					for k, v := range want {
						if got := states[0].Attributes[k]; got != v {
							t.Errorf("imported %s = %q, want %q", k, got, v)
						}
					}
					if _, ok := states[0].Attributes["unique_id"]; ok {
						t.Errorf("imported unique_id = %q, want null", states[0].Attributes["unique_id"])
					}
					return nil
				},
			},
			{
				// The steps cannot be read back, so writing them after the import only records them.
				Config: planeConfig(entryID, 25, 10000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectSensitiveValue(addr, tfjsonpath.New("sensitive_steps")),
					},
				},
				Check: resource.TestCheckResourceAttr(addr, "subentry_id", autoPlane),
			},
			{
				Config: planeConfig(entryID, 25, 10000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A plane has a reconfigure flow: changed steps change it in place. Its reconfigure
				// form has the step_id `reconfigure` and is answered from the fields of `user`.
				Config: planeConfig(entryID, 30, 5000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("title")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "subentry_id", autoPlane),
					resource.TestCheckResourceAttr(addr, "id", entryID+"/"+autoPlane),
					resource.TestCheckResourceAttr(addr, "title", "30° / 180° / 5000W"),
					resource.TestCheckNoResourceAttr(addr, "unique_id"),
					checkOnlyPlane(t, entryID, func() string { return autoPlane }, "30° / 180° / 5000W"),
				),
			},
			{
				Config: planeConfig(entryID, 30, 5000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Deleted outside Tofu: the plane drops out of state and is created again. The entry
				// has no plane then, so the new one needs no API key.
				PreConfig: func() {
					if err := configSubentries(t).Delete(context.Background(), entryID, autoPlane); err != nil {
						t.Fatal(err)
					}
				},
				Config: planeConfig(entryID, 30, 5000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "subentry_type", "plane"),
					resource.TestCheckResourceAttr(addr, "title", "30° / 180° / 5000W"),
					resource.TestCheckResourceAttr(addr, "supports_reconfigure", "true"),
					func(s *terraform.State) error {
						attrs := s.RootModule().Resources[addr].Primary.Attributes
						planeID = attrs["subentry_id"]
						if planeID == autoPlane || attrs["id"] != entryID+"/"+planeID {
							t.Errorf("subentry_id = %q, id = %q; want a new plane", planeID, attrs["id"])
						}
						return nil
					},
					checkOnlyPlane(t, entryID, func() string { return planeID }, "30° / 180° / 5000W"),
				),
			},
			{
				// A created plane is reconfigured in place too.
				Config: planeConfig(entryID, 35, 5000),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(addr, "subentry_id", &planeID),
					resource.TestCheckResourceAttr(addr, "title", "35° / 180° / 5000W"),
					checkOnlyPlane(t, entryID, func() string { return planeID }, "35° / 180° / 5000W"),
				),
			},
		},
	})
}

func TestAccIntegrationSubentry_Errors(t *testing.T) {
	acctest.SharedInstance(t)
	entryID, _ := solarEntry(t)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + fmt.Sprintf(`
resource "homeassistant_integration_subentry" "wrong_type" {
  entry_id      = %q
  subentry_type = "roof"
  steps         = { user = {} }
}
`, entryID),
				ExpectError: regexp.MustCompile(`has no subentry type "roof"`),
			},
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration_subentry" "no_entry" {
  entry_id      = "01JNOSUCHENTRY0000000000000"
  subentry_type = "plane"
  steps         = { user = {} }
}
`,
				ExpectError: regexp.MustCompile(`Check\s+` + "`entry_id`"),
			},
			{
				// Wrong import IDs.
				Config: acctest.ProviderConfig + `
resource "homeassistant_integration_subentry" "imported" {
  entry_id      = "x"
  subentry_type = "plane"
  steps         = { user = {} }
}
`,
				ResourceName:  "homeassistant_integration_subentry.imported",
				ImportState:   true,
				ImportStateId: entryID,
				ExpectError:   regexp.MustCompile(`expected\s+` + "`<entry_id>/<subentry_id>`"),
			},
		},
	})
}
