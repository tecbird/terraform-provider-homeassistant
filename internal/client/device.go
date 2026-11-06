package client

// Device is an entry of the device registry. Devices are created by integrations; the registry
// has no `create` or `delete` command. Some integrations report numeric registry values (a
// firmware version, an identifier component), so RegistryString keeps those readable as strings.
type Device struct {
	ID            string             `json:"id"`
	Name          *string            `json:"name"`
	NameByUser    *string            `json:"name_by_user"`
	Manufacturer  *string            `json:"manufacturer"`
	Model         *string            `json:"model"`
	SWVersion     *RegistryString    `json:"sw_version"`
	HWVersion     *RegistryString    `json:"hw_version"`
	AreaID        *string            `json:"area_id"`
	Labels        []string           `json:"labels"`
	DisabledBy    *string            `json:"disabled_by"`
	Identifiers   [][]RegistryString `json:"identifiers"`
	ConfigEntries []string           `json:"config_entries"`
}

// Devices returns the device registry (`config/device_registry/*`). Only List, Get, and Update
// apply to it.
func Devices(ws WSCommander) Registry[Device] {
	return Registry[Device]{
		ws:     ws,
		prefix: "config/device_registry",
		key:    "device_id",
		idOf:   func(d Device) string { return d.ID },
	}
}
