package handlers

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const zeroTierInstallHelper = "/usr/local/sbin/aircoins-install-zerotier"

type hostZeroTierNetwork struct {
	ID     string
	Name   string
	Type   string
	Status string
	IPs    []string
}

type hostZeroTierStatus struct {
	OSName       string
	OSID         string
	OSVersion    string
	Architecture string
	SupportedOS  bool
	Installed    bool
	HelperReady  bool
	Running      bool
	Online       bool
	NodeID       string
	Version      string
	Networks     []hostZeroTierNetwork
	Error        string
}

type hostToolsPage struct {
	page
	ZeroTier hostZeroTierStatus
}

func detectHostOS() (name, id, version, architecture string, supported bool) {
	if output, err := exec.Command("uname", "-m").Output(); err == nil {
		architecture = strings.TrimSpace(string(output))
	} else {
		architecture = runtime.GOARCH
	}
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS, runtime.GOOS, "", architecture, false
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if ok {
			values[key] = strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	id = values["ID"]
	supported = id == "debian" || id == "ubuntu" || id == "armbian"
	return values["PRETTY_NAME"], id, values["VERSION_ID"], architecture, supported
}

func readHostZeroTierStatus(ctx context.Context) hostZeroTierStatus {
	name, id, version, arch, supported := detectHostOS()
	result := hostZeroTierStatus{OSName: name, OSID: id, OSVersion: version, Architecture: arch, SupportedOS: supported}
	if !supported {
		result.Error = "ZeroTier management is supported on Debian, Ubuntu and Armbian hosts."
		return result
	}
	if _, err := os.Stat(zeroTierInstallHelper); err == nil {
		result.HelperReady = true
	}
	cli, err := exec.LookPath("zerotier-cli")
	if err != nil {
		return result
	}
	result.Installed = true
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "zerotier-one").Output(); err == nil {
		result.Running = strings.TrimSpace(string(output)) == "active"
	}
	output, err := exec.CommandContext(ctx, cli, "-j", "info").Output()
	if err != nil {
		result.Error = "zerotier-cli is installed, but its service did not return status."
		return result
	}
	var info struct {
		Address string `json:"address"`
		Online  bool   `json:"online"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &info); err != nil {
		result.Error = "could not read zerotier-cli status."
		return result
	}
	result.NodeID, result.Online, result.Version = info.Address, info.Online, info.Version
	output, err = exec.CommandContext(ctx, cli, "-j", "listnetworks").Output()
	if err != nil {
		return result
	}
	var rows []struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Type      string          `json:"type"`
		Status    string          `json:"status"`
		IPAddress json.RawMessage `json:"ipAddress"`
	}
	if json.Unmarshal(output, &rows) != nil {
		return result
	}
	for _, row := range rows {
		result.Networks = append(result.Networks, hostZeroTierNetwork{
			ID: row.ID, Name: row.Name, Type: row.Type, Status: row.Status, IPs: zeroTierIPs(row.IPAddress),
		})
	}
	return result
}

func zeroTierIPs(raw json.RawMessage) []string {
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && strings.TrimSpace(one) != "" {
		return []string{one}
	}
	return nil
}

func validHostZeroTierNetworkID(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// Tools manages utilities on the Debian-family machine running the panel.
func (h *Handler) Tools(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	h.render(w, r, http.StatusOK, "tools.html", &hostToolsPage{
		page: page{Title: "Tools", Nav: "tools"}, ZeroTier: readHostZeroTierStatus(ctx),
	})
}

func (h *Handler) ToolsZeroTierInstall(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "linux" {
		h.flashAndRedirect(w, r, "/tools", "err", "ZeroTier installation is only available on the Linux panel host.")
		return
	}
	if _, err := os.Stat(zeroTierInstallHelper); err != nil {
		h.flashAndRedirect(w, r, "/tools", "err", "The Aircoins ZeroTier installer helper is missing. Rerun install.sh to provision it.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper).CombinedOutput()
	if err != nil {
		detail := truncateText(strings.TrimSpace(string(output)), 300)
		h.log.Error("host ZeroTier installation failed", "error", err, "output", detail)
		message := "ZeroTier installation failed. Check the controller log and verify the aircoins sudo rule."
		if detail != "" {
			message += " Details: " + detail
		}
		h.flashAndRedirect(w, r, "/tools", "err", message)
		return
	}
	h.flashAndRedirect(w, r, "/tools", "ok", "ZeroTier installation completed on the panel host.")
}

func (h *Handler) runZeroTierNetworkAction(w http.ResponseWriter, r *http.Request, action, success string) {
	networkID := strings.ToLower(strings.TrimSpace(r.PostFormValue("network_id")))
	if !validHostZeroTierNetworkID(networkID) {
		h.flashAndRedirect(w, r, "/tools", "err", "Enter a 16-character hexadecimal ZeroTier network ID.")
		return
	}
	cli, err := exec.LookPath("zerotier-cli")
	if err != nil {
		h.flashAndRedirect(w, r, "/tools", "err", "Install ZeroTier before managing networks.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, cli, action, networkID).CombinedOutput()
	if err != nil {
		h.log.Error("host ZeroTier action failed", "action", action, "error", err, "output", strings.TrimSpace(string(output)))
		h.flashAndRedirect(w, r, "/tools", "err", "ZeroTier could not "+action+" network "+networkID+".")
		return
	}
	h.flashAndRedirect(w, r, "/tools", "ok", success+" "+networkID+".")
}

func (h *Handler) ToolsZeroTierJoin(w http.ResponseWriter, r *http.Request) {
	h.runZeroTierNetworkAction(w, r, "join", "This host joined ZeroTier network")
}

func (h *Handler) ToolsZeroTierLeave(w http.ResponseWriter, r *http.Request) {
	h.runZeroTierNetworkAction(w, r, "leave", "This host left ZeroTier network")
}
