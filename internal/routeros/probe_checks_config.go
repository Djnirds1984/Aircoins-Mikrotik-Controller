package routeros

import (
	"context"
	"fmt"
	"strings"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// checkLoginMethods verifies that the hotspot profile accepts the PAP style
// login the panel performs over the API.
func (p *prober) checkLoginMethods(ctx context.Context) {
	p.check(ctx, "login_methods", "Login methods allow panel login", func(ctx context.Context) (domain.CheckStatus, string, string) {
		caps := &p.rep.Caps

		if !caps.HotspotMenuPresent {
			return domain.StatusSkip, "skipped: the hotspot menu is unavailable", ""
		}

		methods := caps.LoginMethods
		if methods == "" {
			return domain.StatusWarn,
				"no hotspot server profile was found, so login-by could not be checked",
				"Create a hotspot server on the router (which creates a profile), then re-run the test."
		}

		if SupportsPAPLogin(methods) {
			note := ""
			if !SupportsHTTPChap(methods) {
				note = " (http-chap is off, which is fine here)"
			}
			return domain.StatusPass,
				fmt.Sprintf("login-by=%s includes http-pap, so panel driven logins will work%s", methods, note),
				""
		}

		if SupportsHTTPChap(methods) {
			return domain.StatusFail,
				fmt.Sprintf("login-by=%s has http-chap but not http-pap", methods),
				"Panel driven logins send the password over the API, which is PAP. Add http-pap: /ip hotspot profile set [find name=<profile>] login-by=http-pap,cookie,trial (keep any other methods you rely on)."
		}

		return domain.StatusFail,
			fmt.Sprintf("login-by=%s does not include http-pap", methods),
			"Add http-pap to the hotspot profile's login-by list."
	})
}

// checkAPIService reports which management services are enabled and whether the
// API service restricts source addresses (which could lock the panel out later).
func (p *prober) checkAPIService(ctx context.Context) {
	p.check(ctx, "api_service", "API service settings", func(ctx context.Context) (domain.CheckStatus, string, string) {
		services, err := ReadServices(ctx, p.t)
		if err != nil {
			return domain.StatusWarn, "could not read /ip/service: " + err.Error(),
				"Confirm the API account may read /ip/service."
		}

		var enabled, restricted []string
		for _, svc := range services {
			if svc.Name != "api" && svc.Name != "api-ssl" {
				continue
			}
			if svc.Disabled {
				continue
			}
			enabled = append(enabled, fmt.Sprintf("%s:%d", svc.Name, svc.Port))
			switch svc.Name {
			case "api":
				p.rep.Caps.APIEnabled = true
			case "api-ssl":
				p.rep.Caps.APISSLEnabled = true
			}
			if addr := strings.TrimSpace(svc.Address); addr != "" && addr != "0.0.0.0/0" {
				restricted = append(restricted, svc.Name+" limited to "+addr)
			}
		}

		if len(enabled) == 0 {
			return domain.StatusWarn, "neither api nor api-ssl was reported as enabled",
				"The panel is connected, so one of them must be reachable. Re-check /ip service print."
		}

		summary := "enabled: " + strings.Join(enabled, ", ")
		if len(restricted) > 0 {
			return domain.StatusWarn, summary + "; " + strings.Join(restricted, "; "),
				"A source address restriction can lock this panel out if its address changes. Consider adding the panel's subnet instead of a single address."
		}
		return domain.StatusPass, summary, ""
	})
}

// checkDeviceMode verifies that RouterOS device-mode permits hotspot.
func (p *prober) checkDeviceMode(ctx context.Context) {
	p.check(ctx, "device_mode", "device-mode allows hotspot", func(ctx context.Context) (domain.CheckStatus, string, string) {
		mode, err := ReadDeviceMode(ctx, p.t)
		if err != nil {
			return domain.StatusWarn, "could not read /system/device-mode: " + err.Error(),
				"Confirm the API account may read /system/device-mode."
		}
		if !mode.Present {
			return domain.StatusPass,
				"device-mode is not present on this RouterOS version, so hotspot is unrestricted",
				""
		}

		caps := &p.rep.Caps
		caps.DeviceModeProbed = true

		allowed, known := mode.Allows("hotspot")
		caps.DeviceModeHotspotAllowed = allowed

		if !known {
			return domain.StatusWarn,
				fmt.Sprintf("device-mode is %q but does not report a hotspot flag", mode.Mode),
				"If hotspot fails to start, check /system/device-mode/print on the device console."
		}
		if !allowed {
			return domain.StatusFail,
				fmt.Sprintf("device-mode is %q with hotspot=no, so hotspot cannot run", mode.Mode),
				"Enable it from the console (not over the API): /system/device-mode/update hotspot=yes, then confirm within the timeout."
		}
		return domain.StatusPass, fmt.Sprintf("device-mode is %q and hotspot is allowed", mode.Mode), ""
	})
}
