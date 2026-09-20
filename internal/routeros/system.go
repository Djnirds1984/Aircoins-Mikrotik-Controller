package routeros

import (
	"context"
	"errors"
	"strings"
)

// Identity is /system/identity/print.
type Identity struct {
	Name string
}

// Resource is /system/resource/print.
type Resource struct {
	Uptime           string
	UptimeSeconds    int64
	Version          string
	BuildTime        string
	FactorySoftware  string
	FreeMemory       int64
	TotalMemory      int64
	CPUCount         int
	CPU              string
	FreeHDDSpace     int64
	TotalHDDSpace    int64
	BoardName        string
	ArchitectureName string
	Platform         string
}

// Board is /system/routerboard/print. It is absent on CHR and x86.
type Board struct {
	Present         bool
	Name            string
	Model           string
	SerialNumber    string
	Firmware        string
	FactoryFirmware string
}

// Clock is /system/clock/print.
type Clock struct {
	Date             string
	Time             string
	TimeZoneName     string
	GMTOffset        string
	GMTOffsetSeconds int
}

// NTP is /system/ntp/client/print.
type NTP struct {
	Present    bool
	Enabled    bool
	Server     string
	Mode       string
	SyncedFrom string
}

// License is /system/license/print. Only CHR and x86 report it.
type License struct {
	Present    bool
	Level      int
	Features   string
	SoftwareID string
	Expired    bool
}

// ReadIdentity runs /system/identity/print.
func ReadIdentity(ctx context.Context, t Transport) (Identity, error) {
	reply, err := t.Run(ctx, "/system/identity/print")
	if err != nil {
		return Identity{}, err
	}
	row := reply.First()
	if row == nil {
		return Identity{}, nil
	}
	return Identity{Name: row["name"]}, nil
}

// ReadResource runs /system/resource/print.
func ReadResource(ctx context.Context, t Transport) (Resource, error) {
	reply, err := t.Run(ctx, "/system/resource/print")
	if err != nil {
		return Resource{}, err
	}
	row := reply.First()
	if row == nil {
		return Resource{}, nil
	}
	return Resource{
		Uptime:           row["uptime"],
		UptimeSeconds:    ParseUptime(row["uptime"]),
		Version:          row["version"],
		BuildTime:        row["build-time"],
		FactorySoftware:  row["factory-software"],
		FreeMemory:       ParseSize(row["free-memory"]),
		TotalMemory:      ParseSize(row["total-memory"]),
		CPUCount:         int(ParseInt(row["cpu-count"])),
		CPU:              row["cpu"],
		FreeHDDSpace:     ParseSize(row["free-hdd-space"]),
		TotalHDDSpace:    ParseSize(row["total-hdd-space"]),
		BoardName:        row["board-name"],
		ArchitectureName: row["architecture-name"],
		Platform:         row["platform"],
	}, nil
}

// ReadBoard runs /system/routerboard/print. A missing menu is not an error:
// CHR and x86 installs have no routerboard.
func ReadBoard(ctx context.Context, t Transport) (Board, error) {
	reply, err := t.Run(ctx, "/system/routerboard/print")
	if err != nil {
		if isMissingMenu(err) {
			return Board{Present: false}, nil
		}
		return Board{}, err
	}
	row := reply.First()
	if row == nil {
		return Board{Present: false}, nil
	}
	return Board{
		Present:         true,
		Name:            row["name"],
		Model:           row["model"],
		SerialNumber:    row["serial-number"],
		Firmware:        row["current-firmware"],
		FactoryFirmware: row["factory-firmware"],
	}, nil
}

// ReadClock runs /system/clock/print.
func ReadClock(ctx context.Context, t Transport) (Clock, error) {
	reply, err := t.Run(ctx, "/system/clock/print")
	if err != nil {
		return Clock{}, err
	}
	row := reply.First()
	if row == nil {
		return Clock{}, nil
	}
	return Clock{
		Date:             row["date"],
		Time:             row["time"],
		TimeZoneName:     row["time-zone-name"],
		GMTOffset:        row["gmt-offset"],
		GMTOffsetSeconds: ParseOffsetSeconds(row["gmt-offset"]),
	}, nil
}

// ReadNTP runs /system/ntp/client/print, tolerating a missing menu.
func ReadNTP(ctx context.Context, t Transport) (NTP, error) {
	reply, err := t.Run(ctx, "/system/ntp/client/print")
	if err != nil {
		if isMissingMenu(err) {
			return NTP{Present: false}, nil
		}
		return NTP{}, err
	}
	row := reply.First()
	if row == nil {
		return NTP{Present: false}, nil
	}
	return NTP{
		Present:    true,
		Enabled:    ParseBool(row["enabled"]),
		Server:     row["server"],
		Mode:       row["mode"],
		SyncedFrom: row["synced-from"],
	}, nil
}

// ReadLicense runs /system/license/print, tolerating a missing menu.
func ReadLicense(ctx context.Context, t Transport) (License, error) {
	reply, err := t.Run(ctx, "/system/license/print")
	if err != nil {
		if isMissingMenu(err) {
			return License{Present: false}, nil
		}
		return License{}, err
	}
	row := reply.First()
	if row == nil {
		return License{Present: false}, nil
	}
	return License{
		Present:    true,
		Level:      int(ParseInt(row["level"])),
		Features:   row["features"],
		SoftwareID: row["software-id"],
		Expired:    ParseBool(row["expired"]),
	}, nil
}

// isMissingMenu reports whether an error means "this command does not exist".
func isMissingMenu(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoMenu) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such command") ||
		strings.Contains(msg, "bad command name") ||
		strings.Contains(msg, "unknown command")
}
