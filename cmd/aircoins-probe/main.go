// Command aircoins-probe tests connectivity to a MikroTik RouterOS device.
//
// It runs exactly the same probe ladder that the admin panel uses behind its
// "Test Connection" button, so its output doubles as a field diagnostic tool and
// as the Phase 0 validation harness for this project.
//
// Examples:
//
//	aircoins-probe -host 192.168.88.1 -user admin -pass secret
//	aircoins-probe -host 192.168.88.1 -user admin -pass secret -allow-write
//	aircoins-probe -demo=ok -json
//	aircoins-probe -demo=http-chap
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/routeros/faketos"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/version"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		host       = flag.String("host", "", "router address, e.g. 192.168.88.1")
		port       = flag.Int("port", 8728, "RouterOS API port (8728 plain, 8729 TLS)")
		user       = flag.String("user", "admin", "API username")
		pass       = flag.String("pass", "", "API password")
		useTLS     = flag.Bool("tls", false, "use API-SSL (TLS)")
		timeout    = flag.Duration("timeout", 15*time.Second, "overall probe timeout")
		allowWrite = flag.Bool("allow-write", false, "run the write-permission check (creates and removes a scratch walled-garden entry)")
		asJSON     = flag.Bool("json", false, "emit the report as JSON")
		demo       = flag.String("demo", "", "use a simulated device with the named scenario: ok, no-hotspot, read-only, device-mode, http-chap, small-flash, clock-skew")
	)
	flag.Parse()

	creds := domain.RouterCredentials{
		Host:     *host,
		Port:     *port,
		TLS:      *useTLS,
		User:     *user,
		Password: *pass,
	}

	prober := routeros.NewProber()

	if *demo != "" {
		device, err := demoDevice(*demo, creds.Endpoint())
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
		creds.Host = "demo.local"
		prober.Dial = func(context.Context, routeros.Options) (routeros.Transport, error) {
			return device, nil
		}
		prober.DialTCP = func(context.Context, string, time.Duration) error { return nil }
	}

	if *host == "" && *demo == "" {
		fmt.Fprintln(os.Stderr, "error: -host is required (or use -demo)")
		flag.Usage()
		return 2
	}

	ctx := context.Background()
	report, err := prober.Probe(ctx, routeros.ProbeOptions{
		Credentials: creds,
		Timeout:     *timeout,
		AllowWrite:  *allowWrite,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "probe failed:", err)
		return 2
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "encode report:", err)
			return 2
		}
	} else {
		printReport(report)
	}

	if report.HasFailures() {
		return 1
	}
	return 0
}

// demoDevice builds a simulated device for the named scenario.
func demoDevice(scenario, address string) (*faketos.Device, error) {
	opts := faketos.Options{Host: address}

	switch scenario {
	case "ok":
	case "no-hotspot":
		opts.SkipHotspot = true
	case "read-only":
		opts.ReadOnly = true
	case "device-mode":
		opts.BlockHotspotDeviceMode = true
	case "http-chap":
		opts.LoginBy = "http-chap,cookie"
	case "small-flash":
		opts.FreeHDD = 64 * 1024
	case "clock-skew":
		opts.ClockOffsetHours = 3
	default:
		return nil, fmt.Errorf("unknown demo scenario %q", scenario)
	}

	return faketos.New(opts), nil
}

func printReport(r *domain.ProbeReport) {
	fmt.Printf("Aircoins probe %s\n", version.String())
	fmt.Printf("Target : %s\n", r.Address)
	fmt.Printf("Result : %s (%d ms)\n\n", r.Result, r.LatencyMS)

	fmt.Printf("%-6s %-34s %s\n", "STATUS", "CHECK", "DETAIL")
	fmt.Println(strings.Repeat("-", 100))
	for _, c := range r.Checks {
		fmt.Printf("%-6s %-34s %s\n", c.Status, truncate(c.Title, 34), c.Message)
	}

	var fixes []domain.ProbeCheck
	for _, c := range r.Checks {
		if c.Fix != "" {
			fixes = append(fixes, c)
		}
	}
	if len(fixes) > 0 {
		fmt.Println()
		fmt.Println("What to do next")
		for _, c := range fixes {
			fmt.Printf("  [%s] %s\n      %s\n", c.Status, c.Title, c.Fix)
		}
	}

	caps := r.Caps
	fmt.Println()
	fmt.Println("Capabilities")
	fmt.Printf("  %-22s %s\n", "identity", orDash(caps.Identity))
	fmt.Printf("  %-22s %s\n", "RouterOS", orDash(caps.ROSVersion))
	fmt.Printf("  %-22s %s / %s\n", "board", orDash(caps.BoardName), orDash(caps.Arch))
	fmt.Printf("  %-22s %s\n", "hotspot menu", yesNo(caps.HotspotMenuPresent))
	fmt.Printf("  %-22s %s\n", "hotspot servers", listOrDash(caps.HotspotServers))
	fmt.Printf("  %-22s %s\n", "user profiles", listOrDash(caps.HotspotUserProfiles))
	fmt.Printf("  %-22s %s\n", "login-by", orDash(caps.LoginMethods))
	fmt.Printf("  %-22s %s\n", "hotspot users", fmt.Sprint(caps.HotspotUsers))
	fmt.Printf("  %-22s %s\n", "active sessions", fmt.Sprint(caps.ActiveSessions))
	fmt.Printf("  %-22s %s\n", "api / api-ssl", fmt.Sprintf("%s / %s", yesNo(caps.APIEnabled), yesNo(caps.APISSLEnabled)))
	fmt.Printf("  %-22s %s\n", "api writable", yesNo(caps.APIWritable))
	fmt.Printf("  %-22s %s\n", "free flash", routeros.FormatBytes(caps.FreeHDDSpace))
	fmt.Printf("  %-22s %s\n", "user capacity", fmt.Sprintf("~%d", caps.EstimatedUserCapacity))
	fmt.Printf("  %-22s %d s\n", "clock offset", caps.ClockOffsetSec)
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "..."
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func listOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
