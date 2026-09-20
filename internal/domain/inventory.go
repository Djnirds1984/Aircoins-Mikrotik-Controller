package domain

import "time"

// HotspotServerCache is a cached hotspot server entry read from a router. The
// live inventory always comes from the device; this cache exists so list and
// detail pages do not need a router round trip.
type HotspotServerCache struct {
	Name        string
	Interface   string
	AddressPool string
	Profile     string
	Disabled    bool
	Raw         string
	SyncedAt    time.Time
}
