// Package faketos implements a simulated RouterOS device.
//
// It exists so the panel, the probe ladder and the router registry can be
// developed and demonstrated without physical hardware. It implements
// routeros.Transport, so it can be injected anywhere the real API client is
// used, including pessimistic scenarios such as a locked-down account or a
// router without the hotspot package.
package faketos

import "sync"

// Options configure the simulated device.
type Options struct {
	// Host is reported by Host() and used in messages.
	Host string
	// ROSVersion is the version reported by /system/resource.
	ROSVersion string
	// BoardName, Model and Arch describe the simulated hardware.
	BoardName string
	Model     string
	Arch      string
	// LoginBy is the hotspot profile login-by list.
	LoginBy string
	// SkipHotspot true simulates a device where /ip hotspot does not exist,
	// which is what a router without the hotspot package reports.
	SkipHotspot bool
	// ReadOnly true simulates an API account that may read but not write.
	ReadOnly bool
	// ClockOffsetHours shifts the simulated device clock.
	ClockOffsetHours int
	// FreeHDD is the free flash reported by /system/resource.
	FreeHDD int64
	// BlockHotspotDeviceMode true simulates device-mode blocking hotspot.
	BlockHotspotDeviceMode bool
}

// Device is a simulated RouterOS device.
type Device struct {
	opts Options

	mu        sync.Mutex
	nextID    int
	users     []map[string]string
	profiles  []map[string]string
	passwords map[string]string
	walled    []map[string]string
}

// New returns a simulated device with sensible defaults.
func New(opts Options) *Device {
	if opts.Host == "" {
		opts.Host = "192.168.88.1:8728"
	}
	if opts.ROSVersion == "" {
		opts.ROSVersion = "7.16.2 (stable)"
	}
	if opts.BoardName == "" {
		opts.BoardName = "hAP ac^2"
	}
	if opts.Model == "" {
		opts.Model = "RBD52G-5HacD2HnD"
	}
	if opts.Arch == "" {
		opts.Arch = "arm"
	}
	if opts.LoginBy == "" {
		opts.LoginBy = "http-pap,http-chap,cookie,trial"
	}
	if opts.FreeHDD == 0 {
		opts.FreeHDD = 12 * 1024 * 1024
	}
	d := &Device{opts: opts}

	// One demo voucher so user and session lists are not empty.
	d.users = []map[string]string{{
		".id":     "*1",
		"name":    "DEMO-0001",
		"profile": "ac-1h-10M",
		"server":  "all",
		"uptime":  "0s",
	}}
	d.nextID = 2
	d.users = []map[string]string{{
		".id":     "*1",
		"name":    "DEMO-0001",
		"profile": "ac-1h-10M",
		"server":  "all",
		"uptime":  "0s",
	}}
	d.profiles = []map[string]string{
		{".id": "*1", "name": "default", "shared-users": "1"},
		{".id": "*2", "name": "ac-1h-10M", "shared-users": "1", "rate-limit": "10M/10M", "session-timeout": "1h"},
	}
	d.passwords = map[string]string{"DEMO-0001": "demo"}
	return d
}

// Host implements routeros.Transport.
func (d *Device) Host() string { return d.opts.Host }

// Close implements routeros.Transport.
func (d *Device) Close() error { return nil }
