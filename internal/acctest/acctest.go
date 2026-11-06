// Package acctest runs acceptance tests against a real Home Assistant in Docker (ADR-0017).
//
// Each test package that has acceptance tests calls Main from its TestMain and PreCheck from
// every acceptance test. The first PreCheck in a package starts one Home Assistant container,
// onboards it headlessly, and exports its URL and a long-lived token as HOMEASSISTANT_URL and
// HOMEASSISTANT_TOKEN, which the provider reads. Main removes the container when the package's
// tests are done.
//
// Without TF_ACC, terraform-plugin-testing skips acceptance tests before PreCheck runs, so no
// container is started.
package acctest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/toelke/terraform-provider-homeassistant/internal/provider"
)

const (
	// EnvImageTag selects the Home Assistant image tag. CI sets it to the oldest and the newest
	// release of the support window (ADR-0003).
	EnvImageTag = "HOMEASSISTANT_IMAGE_TAG"
	// DefaultImageTag is used when EnvImageTag is unset. Keep it equal to the newest tag of the
	// CI matrix in .github/workflows/acceptance.yml.
	DefaultImageTag = "2026.11.0"

	image          = "ghcr.io/home-assistant/home-assistant"
	startupTimeout = 5 * time.Minute
)

// ProviderConfig pins the provider address. Start every test configuration with it, so that
// OpenTofu resolves `homeassistant` to the provider under test.
const ProviderConfig = `
terraform {
  required_providers {
    homeassistant = {
      source = "toelke/homeassistant"
    }
  }
}
`

// ProtoV6ProviderFactories serves the provider in-process to OpenTofu.
var ProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"homeassistant": providerserver.NewProtocol6WithError(provider.New("acctest")()),
}

// Instance is an onboarded Home Assistant.
type Instance struct {
	// URL is the base URL, without `/api`.
	URL string
	// Token is a long-lived access token of the owner user.
	Token string
	// ImageTag is the Home Assistant image tag the container runs.
	ImageTag string
}

var (
	startOnce sync.Once
	shared    *Instance
	startErr  error
	container testcontainers.Container
)

// Main runs the package's tests and then removes the shared container, if one was started.
// Call it from TestMain.
func Main(m *testing.M) {
	code := m.Run()
	if container != nil {
		if err := container.Terminate(context.Background(), testcontainers.StopTimeout(5*time.Second)); err != nil {
			fmt.Fprintf(os.Stderr, "acctest: terminate Home Assistant container: %v\n", err)
		}
	}
	os.Exit(code)
}

// PreCheck makes sure the shared Home Assistant is running and the provider points at it. Use it
// as the PreCheck of every acceptance test.
func PreCheck(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC_TERRAFORM_PATH") == "" {
		t.Fatal("TF_ACC_TERRAFORM_PATH must point to the OpenTofu binary, e.g. TF_ACC_TERRAFORM_PATH=$(which tofu)")
	}
	SharedInstance(t)
}

// SharedInstance returns the package's Home Assistant, starting and onboarding it on first use.
// Without TF_ACC, it skips the test.
func SharedInstance(t *testing.T) *Instance {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests need TF_ACC=1 and Docker")
	}
	startOnce.Do(func() {
		shared, startErr = start(context.Background())
		if startErr == nil {
			// The provider runs in this process and reads its configuration from the environment.
			startErr = setEnv(map[string]string{
				"HOMEASSISTANT_URL":   shared.URL,
				"HOMEASSISTANT_TOKEN": shared.Token,
			})
		}
	})
	if startErr != nil {
		t.Fatalf("start Home Assistant: %v", startErr)
	}
	return shared
}

func start(ctx context.Context) (*Instance, error) {
	tag := os.Getenv(EnvImageTag)
	if tag == "" {
		tag = DefaultImageTag
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image + ":" + tag,
			ExposedPorts: []string{"8123/tcp"},
			Env:          map[string]string{"TZ": "UTC"},
			WaitingFor: wait.ForHTTP("/api/onboarding").
				WithPort("8123/tcp").
				WithStartupTimeout(startupTimeout),
		},
		Started: true,
	})
	container = c // set even on error, so Main can clean up a half-started container
	if err != nil {
		return nil, err
	}

	endpoint, err := c.PortEndpoint(ctx, "8123/tcp", "http")
	if err != nil {
		return nil, err
	}
	onboardCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	token, err := onboard(onboardCtx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("onboard %s: %w", endpoint, err)
	}
	return &Instance{URL: endpoint, Token: token, ImageTag: tag}, nil
}

func setEnv(vars map[string]string) error {
	for k, v := range vars {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
