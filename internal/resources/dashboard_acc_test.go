package resources_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/toelke/terraform-provider-homeassistant/internal/acctest"
	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

func dashboards(t *testing.T) client.Dashboards {
	t.Helper()
	return client.NewDashboards(haClient(t).WS)
}

// checkDashboardListed fails unless `lovelace/dashboards/list` has the dashboard with the given
// title.
func checkDashboardListed(t *testing.T, urlPath, title string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		d, ok, err := dashboards(t).Get(context.Background(), urlPath)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("dashboard %q is not in lovelace/dashboards/list", urlPath)
		}
		if d.Title != title {
			return fmt.Errorf("dashboard %q has title %q, want %q", urlPath, d.Title, title)
		}
		return nil
	}
}

// checkDashboardConfig fails unless HA's saved config of the dashboard means the same as want.
func checkDashboardConfig(t *testing.T, urlPath, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := dashboards(t).Config(context.Background(), urlPath)
		if err != nil {
			return err
		}
		eq, err := dyntype.SemanticallyEqualJSON(got, []byte(want))
		if err != nil {
			return err
		}
		if !eq {
			return fmt.Errorf("dashboard %q config = %s, want %s", urlPath, got, want)
		}
		return nil
	}
}

func checkDashboardGone(t *testing.T, urlPath string) func(*terraform.State) error {
	return func(*terraform.State) error {
		if _, ok, err := dashboards(t).Get(context.Background(), urlPath); err != nil || ok {
			return fmt.Errorf("dashboard %q: exists = %v, err = %w; want it gone", urlPath, ok, err)
		}
		return nil
	}
}

// expectUnchanged is a plan check that fails if the planned change touches the attribute.
type expectUnchanged struct {
	addr, attr string
}

func (e expectUnchanged) CheckPlan(_ context.Context, req plancheck.CheckPlanRequest, resp *plancheck.CheckPlanResponse) {
	for _, rc := range req.Plan.ResourceChanges {
		if rc.Address != e.addr {
			continue
		}
		before, _ := rc.Change.Before.(map[string]any)
		after, _ := rc.Change.After.(map[string]any)
		if !reflect.DeepEqual(before[e.attr], after[e.attr]) {
			resp.Error = fmt.Errorf("%s.%s changes from %v to %v; want it unchanged", e.addr, e.attr, before[e.attr], after[e.attr])
		}
		return
	}
	resp.Error = fmt.Errorf("%s not in plan", e.addr)
}

const accDashboardConfig = `
locals {
  quick_actions = {
    type  = "grid"
    cards = [
      { type = "heading", heading = "Quick actions" },
      { type = "button", entity = "sun.sun", grid_options = { columns = 6 } },
    ]
  }
}
`

func TestAccDashboard(t *testing.T) {
	const addr = "homeassistant_dashboard.test"
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardGone(t, "acc-dash"),
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + accDashboardConfig + `
resource "homeassistant_dashboard" "test" {
  url_path = "acc-dash"
  title    = "Acc Dash"
  icon     = "mdi:home-variant"
  config = {
    views = [{ type = "sections", title = "Overview", sections = [local.quick_actions] }]
  }
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", "acc-dash"),
					resource.TestCheckResourceAttr(addr, "url_path", "acc-dash"),
					resource.TestCheckResourceAttr(addr, "dashboard_id", "acc_dash"),
					resource.TestCheckResourceAttr(addr, "title", "Acc Dash"),
					resource.TestCheckResourceAttr(addr, "icon", "mdi:home-variant"),
					resource.TestCheckResourceAttr(addr, "show_in_sidebar", "true"),
					resource.TestCheckResourceAttr(addr, "require_admin", "false"),
					checkDashboardListed(t, "acc-dash", "Acc Dash"),
					checkDashboardConfig(t, "acc-dash", `{"views":[{"type":"sections","title":"Overview","sections":[
						{"type":"grid","cards":[
							{"type":"heading","heading":"Quick actions"},
							{"type":"button","entity":"sun.sun","grid_options":{"columns":6}}]}]}]}`),
				),
			},
			{
				// Metadata only: the config is not part of the change.
				Config: acctest.ProviderConfig + accDashboardConfig + `
resource "homeassistant_dashboard" "test" {
  url_path        = "acc-dash"
  title           = "Acc Dash Renamed"
  icon            = "mdi:home-variant"
  show_in_sidebar = false
  config = {
    views = [{ type = "sections", title = "Overview", sections = [local.quick_actions] }]
  }
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						expectUnchanged{addr, "config"},
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "title", "Acc Dash Renamed"),
					resource.TestCheckResourceAttr(addr, "show_in_sidebar", "false"),
					checkDashboardListed(t, "acc-dash", "Acc Dash Renamed"),
				),
			},
			{
				// Change the config; dropping icon clears it.
				Config: acctest.ProviderConfig + accDashboardConfig + `
resource "homeassistant_dashboard" "test" {
  url_path        = "acc-dash"
  title           = "Acc Dash Renamed"
  show_in_sidebar = false
  config = {
    views = [
      { type = "sections", title = "Overview", sections = [local.quick_actions] },
      { type = "sections", title = "Hallway", sections = [
        local.quick_actions,
        { type = "grid", cards = [{ type = "tile", entity = "sun.sun" }] },
      ] },
    ]
  }
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "icon"),
					checkDashboardConfig(t, "acc-dash", `{"views":[
						{"type":"sections","title":"Overview","sections":[
							{"type":"grid","cards":[
								{"type":"heading","heading":"Quick actions"},
								{"type":"button","entity":"sun.sun","grid_options":{"columns":6}}]}]},
						{"type":"sections","title":"Hallway","sections":[
							{"type":"grid","cards":[
								{"type":"heading","heading":"Quick actions"},
								{"type":"button","entity":"sun.sun","grid_options":{"columns":6}}]},
							{"type":"grid","cards":[{"type":"tile","entity":"sun.sun"}]}]}]}`),
				),
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "acc-dash",
				ImportStateVerify: true,
			},
			{
				// A config edited in the HA UI shows up as drift despite the stored baseline, and the
				// apply puts it back.
				PreConfig: func() {
					if err := dashboards(t).SaveConfig(context.Background(), "acc-dash",
						json.RawMessage(`{"views":[{"title":"Edited in the UI"}]}`)); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + accDashboardConfig + `
resource "homeassistant_dashboard" "test" {
  url_path        = "acc-dash"
  title           = "Acc Dash Renamed"
  show_in_sidebar = false
  config = {
    views = [{ type = "sections", title = "Overview", sections = [local.quick_actions] }]
  }
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: checkDashboardConfig(t, "acc-dash", `{"views":[{"type":"sections","title":"Overview","sections":[
					{"type":"grid","cards":[
						{"type":"heading","heading":"Quick actions"},
						{"type":"button","entity":"sun.sun","grid_options":{"columns":6}}]}]}]}`),
			},
			{
				// Deleted outside Tofu: the dashboard drops out of state and is created again.
				PreConfig: func() {
					if err := dashboards(t).Delete(context.Background(), "acc_dash"); err != nil {
						t.Fatal(err)
					}
				},
				Config: acctest.ProviderConfig + accDashboardConfig + `
resource "homeassistant_dashboard" "test" {
  url_path        = "acc-dash"
  title           = "Acc Dash Renamed"
  show_in_sidebar = false
  config = {
    views = [{ type = "sections", title = "Overview", sections = [local.quick_actions] }]
  }
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)},
				},
				Check: checkDashboardListed(t, "acc-dash", "Acc Dash Renamed"),
			},
		},
	})
}

func TestAccDashboard_URLPathWithoutHyphen(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: acctest.ProviderConfig + `
resource "homeassistant_dashboard" "test" {
  url_path = "home"
  title    = "Home"
  config   = { views = [] }
}
`,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`must contain a hyphen`),
			},
		},
	})
}

// TestAccDashboard_builderFunctions builds a dashboard config with the card builder functions,
// so the arguments pass through OpenTofu's own type conversion.
func TestAccDashboard_builderFunctions(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { acctest.PreCheck(t) },
		ProtoV6ProviderFactories: acctest.ProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardGone(t, "acc-functions"),
		Steps: []resource.TestStep{{
			Config: acctest.ProviderConfig + `
resource "homeassistant_dashboard" "test" {
  url_path = "acc-functions"
  title    = "Acc Functions"
  config = {
    views = [{
      type  = "sections"
      title = "Overview"
      sections = [
        provider::homeassistant::grid_section([
          provider::homeassistant::tile_card("sun.sun", { name = "Sun", icon = null }),
          provider::homeassistant::entities_card(["sun.sun", { entity = "sun.sun", name = "Sun" }]),
          { type = "button", entity = "sun.sun" },
        ], { heading = "Sky" }),
        provider::homeassistant::grid_section(
          [provider::homeassistant::heading_card("Alone")], { column_span = 2 },
        ),
      ]
    }]
  }
}
`,
			Check: checkDashboardConfig(t, "acc-functions", `{"views":[{"type":"sections","title":"Overview","sections":[
				{"type":"grid","cards":[
					{"type":"heading","heading":"Sky"},
					{"type":"tile","entity":"sun.sun","name":"Sun"},
					{"type":"entities","entities":["sun.sun",{"entity":"sun.sun","name":"Sun"}]},
					{"type":"button","entity":"sun.sun"}]},
				{"type":"grid","column_span":2,"cards":[{"type":"heading","heading":"Alone"}]}]}]}`),
		}},
	})
}

func TestAccDashboard_upgrade(t *testing.T) {
	const addr = "homeassistant_dashboard.test"
	acctest.UpgradeTest(t, acctest.ProviderConfig+accDashboardConfig+`
resource "homeassistant_dashboard" "test" {
  url_path = "acc-dash"
  title    = "Acc Dash"
  icon     = "mdi:home-variant"
  config = {
    views = [{ type = "sections", title = "Overview", sections = [local.quick_actions] }]
  }
}
`,
		resource.TestCheckResourceAttr(addr, "id", "acc-dash"),
		resource.TestCheckResourceAttr(addr, "dashboard_id", "acc_dash"),
	)
}
