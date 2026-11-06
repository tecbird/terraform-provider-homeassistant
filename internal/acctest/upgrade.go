package acctest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
)

// ReleasedVersion is the version constraint of the released provider that upgrade tests start
// from: the latest release of the current major version. The first release of a new major
// version updates it.
const ReleasedVersion = "~> 1.0"

// releasedProviderConfig is ProviderConfig with the released provider's version constraint.
var releasedProviderConfig = fmt.Sprintf(`
terraform {
  required_providers {
    homeassistant = {
      source  = "toelke/homeassistant"
      version = %q
    }
  }
}
`, ReleasedVersion)

// UpgradeTest checks that state written by the latest release of the current major version
// works with the build under test. It applies config with the released provider from the
// registry, then plans the same config with the build under test: the plan must be empty, and
// the checks must hold on the refreshed state. config starts with ProviderConfig, like every
// test configuration.
//
// A resource whose state changes on purpose checks the new value in checks. One that cannot plan
// empty after an upgrade needs a state upgrader; if its configuration changes too, it uses
// UpgradeTestTo.
func UpgradeTest(t *testing.T, config string, checks ...resource.TestCheckFunc) {
	t.Helper()
	UpgradeTestTo(t, config, config, checks...)
}

// UpgradeTestTo is UpgradeTest for a breaking change that makes users change their configuration
// (ADR-0026): it applies released with the released provider, then plans current with the build
// under test. The plan must still be empty, so the state written by the release must work with
// the new configuration without any change in Home Assistant. Both configurations start with
// ProviderConfig.
//
// After the first release of the new major version, the test goes back to UpgradeTest with the
// new configuration.
func UpgradeTestTo(t *testing.T, released, current string, checks ...resource.TestCheckFunc) {
	t.Helper()
	body, ok := strings.CutPrefix(released, ProviderConfig)
	if !ok {
		t.Fatal("acctest.UpgradeTestTo: the released config must start with acctest.ProviderConfig")
	}
	if !strings.HasPrefix(current, ProviderConfig) {
		t.Fatal("acctest.UpgradeTestTo: the current config must start with acctest.ProviderConfig")
	}
	resource.Test(t, resource.TestCase{
		PreCheck: func() { PreCheck(t) },
		// The two steps use different providers, so they are set per step.
		Steps: []resource.TestStep{
			{
				ExternalProviders: map[string]resource.ExternalProvider{
					"homeassistant": {Source: "toelke/homeassistant", VersionConstraint: ReleasedVersion},
				},
				// The configuration has a terraform block, so the version constraint must be in it:
				// terraform-plugin-testing adds none from ExternalProviders.
				Config: releasedProviderConfig + body,
			},
			{
				ProtoV6ProviderFactories: ProtoV6ProviderFactories,
				Config:                   current,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(checks...),
			},
		},
	})
}
