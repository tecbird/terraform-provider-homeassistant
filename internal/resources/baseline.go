package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/toelke/terraform-provider-homeassistant/internal/client"
	"github.com/toelke/terraform-provider-homeassistant/internal/dyntype"
)

// The stored baseline (ADR-0023): after each write, the provider reads the config back from HA
// and keeps a hash of it in private state. A later read that hashes the same means nothing
// changed outside Tofu, even where HA rewrote the config on save, e.g. renamed old keys.

// baselineKey is the private state key of the stored baseline.
const baselineKey = "baseline"

// privateGetter and privateSetter are the parts of the framework's private state that the
// baseline needs. Requests and responses carry it as their Private field.
type (
	privateGetter interface {
		GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
	}
	privateSetter interface {
		SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
	}
)

// storeBaseline records config, as HA returned it right after a write, as the stored baseline.
func storeBaseline(ctx context.Context, private privateSetter, config json.RawMessage) diag.Diagnostics {
	hash, err := dyntype.Hash(config)
	if err != nil {
		var diags diag.Diagnostics
		diags.AddError("Storing the baseline", err.Error())
		return diags
	}
	value, _ := json.Marshal(hash)
	return private.SetKey(ctx, baselineKey, value)
}

// storedBaseline returns the raw stored baseline, nil if there is none.
func storedBaseline(ctx context.Context, private privateGetter) ([]byte, diag.Diagnostics) {
	return private.GetKey(ctx, baselineKey)
}

// refreshedConfig returns the config for state after a read: the prior value if the config read
// from HA matches the stored baseline, else the config read. The framework then compares the
// latter with the prior value by semantic equality (ADR-0006). baseline is the raw private state
// value, nil if there is none (state from v0.1.0, or after import).
func refreshedConfig(prior dyntype.Value, read json.RawMessage, baseline []byte) (dyntype.Value, error) {
	if !prior.IsNull() && !prior.IsUnknown() {
		match, err := matchesBaseline(read, baseline)
		if err != nil {
			return dyntype.Value{}, err
		}
		if match {
			return prior, nil
		}
	}
	return dyntype.FromJSON(read)
}

// matchesBaseline reports whether read hashes the same as the raw private state value baseline.
// It is false without a baseline.
func matchesBaseline(read json.RawMessage, baseline []byte) (bool, error) {
	if baseline == nil {
		return false, nil
	}
	var stored string
	if err := json.Unmarshal(baseline, &stored); err != nil {
		return false, fmt.Errorf("decoding the stored baseline: %w", err)
	}
	hash, err := dyntype.Hash(read)
	if err != nil {
		return false, err
	}
	return hash == stored, nil
}

// addSaveError reports err from saving a `config`. Home Assistant rejecting the config (a 400 on
// REST, an error result on the WebSocket) is attached to the `config` attribute; anything else,
// such as an unreachable HA, to the resource.
func addSaveError(diags *diag.Diagnostics, summary string, err error) {
	var httpErr *client.HTTPError
	var wsErr *client.WSError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusBadRequest || errors.As(err, &wsErr) {
		diags.AddAttributeError(path.Root("config"), summary, client.ErrorDetail(err))
		return
	}
	diags.AddError(summary, client.ErrorDetail(err))
}
