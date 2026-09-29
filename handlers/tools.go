package handlers

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const zeroTierInstallHelper = "/usr/local/sbin/aircoins-install-zerotier"

type zeroTierInstallJob struct {
	ID      string
	Percent int
	Message string
	Done    bool
	Failed  bool
	Error   string
}

var (
	zeroTierJobID uint64
	zeroTierJobs  = struct {
		sync.RWMutex
		items map[string]*zeroTierInstallJob
	}{items: make(map[string]*zeroTierInstallJob)}
)

type hostZeroTierNetwork struct {
	ID     string
	Name   string
	Type   string
	Status string
	// Interface is the OS network device ZeroTier created for the network.
	// zerotier-cli reports it as "portDeviceName".
	Interface string
	// IPs holds the addresses shown in the "Interface / IP" column: the
	// ZeroTier-managed assignments when the service reports them, otherwise
	// the addresses the kernel has on the interface.
	IPs []string
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
	ZeroTier   hostZeroTierStatus
	InstallJob *zeroTierInstallJob
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
	_, err := exec.LookPath("zerotier-cli")
	if err != nil {
		return result
	}
	result.Installed = true
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "zerotier-one").Output(); err == nil {
		result.Running = strings.TrimSpace(string(output)) == "active"
	}
	output, err := exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper, "info").CombinedOutput()
	if err != nil {
		detail := truncateText(strings.TrimSpace(string(output)), 200)
		if detail == "" {
			detail = err.Error()
		}
		result.Error = "zerotier-cli is installed, but its service did not return status: " + detail
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
	output, err = exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper, "listnetworks").CombinedOutput()
	if err != nil {
		detail := truncateText(strings.TrimSpace(string(output)), 200)
		if detail == "" {
			detail = err.Error()
		}
		result.Error = "zerotier-cli reported the node status, but the joined networks could not be read: " + detail
		return result
	}
	networks, err := parseHostZeroTierNetworks(output)
	if err != nil {
		result.Error = "could not read the ZeroTier network list returned by zerotier-cli."
		return result
	}
	for i := range networks {
		// assignedAddresses only lists ZeroTier-managed addresses, so fall back
		// to the addresses the kernel has on the interface (gateways, DHCP or
		// manually configured networks leave it empty).
		if len(networks[i].IPs) == 0 {
			networks[i].IPs = hostInterfaceAddresses(ctx, networks[i].Interface)
		}
	}
	result.Networks = networks
	return result
}

// parseHostZeroTierNetworks decodes `zerotier-cli -j listnetworks` (the format
// the aircoins-install-zerotier helper runs). ZeroTier reports the interface as
// "portDeviceName" and the assigned addresses as "assignedAddresses"; older or
// alternative builds use "ipAddress" or "ipAssignments" instead.
func parseHostZeroTierNetworks(output []byte) ([]hostZeroTierNetwork, error) {
	var rows []struct {
		ID              string          `json:"id"`
		NWID            string          `json:"nwid"`
		Name            string          `json:"name"`
		Type            string          `json:"type"`
		Status          string          `json:"status"`
		PortDeviceName  string          `json:"portDeviceName"`
		AssignedAddress json.RawMessage `json:"assignedAddresses"`
		IPAddress       json.RawMessage `json:"ipAddress"`
		IPAssignments   json.RawMessage `json:"ipAssignments"`
	}
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil, err
	}
	networks := make([]hostZeroTierNetwork, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			id = strings.TrimSpace(row.NWID)
		}
		networks = append(networks, hostZeroTierNetwork{
			ID:        id,
			Name:      strings.TrimSpace(row.Name),
			Type:      strings.TrimSpace(row.Type),
			Status:    strings.TrimSpace(row.Status),
			Interface: strings.TrimSpace(row.PortDeviceName),
			IPs:       zeroTierAddresses(row.AssignedAddress, row.IPAddress, row.IPAssignments),
		})
	}
	return networks, nil
}

// zeroTierAddresses collects the addresses of a network row. Every candidate is
// accepted either as a JSON array of strings or as a single string.
func zeroTierAddresses(values ...json.RawMessage) []string {
	var addresses []string
	seen := make(map[string]bool, len(values))
	add := func(address string) {
		if address = strings.TrimSpace(address); address == "" || seen[address] {
			return
		}
		seen[address] = true
		addresses = append(addresses, address)
	}
	for _, raw := range values {
		if len(raw) == 0 {
			continue
		}
		var list []string
		if json.Unmarshal(raw, &list) == nil {
			for _, address := range list {
				add(address)
			}
			continue
		}
		var one string
		if json.Unmarshal(raw, &one) == nil {
			add(one)
		}
	}
	return addresses
}

// hostInterfaceAddresses reads the addresses the kernel has on an interface.
// It is used when ZeroTier reports no managed address for a joined network, so
// the panel can still show the tunnel IP. Link-local IPv6 addresses are skipped
// because every ZeroTier interface has one.
func hostInterfaceAddresses(ctx context.Context, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	output, err := exec.CommandContext(ctx, "ip", "-o", "addr", "show", "dev", name).Output()
	if err != nil {
		return nil
	}
	var addresses []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] != "inet" && fields[i] != "inet6" {
				continue
			}
			address := fields[i+1]
			if address == "" || seen[address] || strings.HasPrefix(strings.ToLower(address), "fe80:") {
				continue
			}
			seen[address] = true
			addresses = append(addresses, address)
		}
	}
	return addresses
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
	view := &hostToolsPage{page: page{Title: "Tools", Nav: "tools"}, ZeroTier: readHostZeroTierStatus(ctx)}
	if id := strings.TrimSpace(r.URL.Query().Get("install_job")); id != "" {
		zeroTierJobs.RLock()
		if job := zeroTierJobs.items[id]; job != nil {
			copy := *job
			view.InstallJob = &copy
		}
		zeroTierJobs.RUnlock()
	}
	h.render(w, r, http.StatusOK, "tools.html", view)
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

	job := &zeroTierInstallJob{ID: strconv.FormatUint(atomic.AddUint64(&zeroTierJobID, 1), 10), Percent: 5, Message: "Starting ZeroTier installation..."}
	zeroTierJobs.Lock()
	zeroTierJobs.items[job.ID] = job
	zeroTierJobs.Unlock()
	go h.runZeroTierInstallJob(job)
	http.Redirect(w, r, "/tools?install_job="+job.ID, http.StatusSeeOther)
}

func (h *Handler) runZeroTierInstallJob(job *zeroTierInstallJob) {
	update := func(percent int, message string) {
		zeroTierJobs.Lock()
		job.Percent, job.Message = percent, message
		zeroTierJobs.Unlock()
	}
	finishFailure := func(err error, output string) {
		detail := truncateText(strings.TrimSpace(output), 300)
		zeroTierJobs.Lock()
		job.Percent, job.Message, job.Done, job.Failed, job.Error = 100, "Installation failed.", true, true, detail
		zeroTierJobs.Unlock()
		h.log.Error("host ZeroTier installation failed", "error", err, "output", detail, "job", job.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	update(20, "Running: curl -s https://install.zerotier.com | sudo bash")
	output, err := exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper).CombinedOutput()
	if err != nil {
		finishFailure(err, string(output))
		return
	}
	// The root helper already runs `systemctl enable --now zerotier-one`.
	// Do not repeat that command as the unprivileged aircoins user.
	update(85, "Verifying the ZeroTier service...")
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "zerotier-one").Run(); err != nil {
		finishFailure(err, "ZeroTier installer returned successfully, but zerotier-one is not active")
		return
	}
	zeroTierJobs.Lock()
	job.Percent, job.Message, job.Done = 100, "ZeroTier installation completed.", true
	zeroTierJobs.Unlock()
}

func ensureHostZeroTierService(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "is-active", "zerotier-one").Output(); err == nil && strings.TrimSpace(string(output)) == "active" {
		return nil
	}
	if _, err := exec.LookPath("zerotier-cli"); err != nil {
		return errors.New("ZeroTier is not installed on this host")
	}
	output, err := exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return errors.New("could not start zerotier-one: " + detail)
	}
	return nil
}

func (h *Handler) runZeroTierNetworkAction(w http.ResponseWriter, r *http.Request, action, success string) {
	networkID := strings.ToLower(strings.TrimSpace(r.PostFormValue("network_id")))
	if !validHostZeroTierNetworkID(networkID) {
		h.flashAndRedirect(w, r, "/tools", "err", "Enter a 16-character hexadecimal ZeroTier network ID.")
		return
	}
	if _, err := exec.LookPath("zerotier-cli"); err != nil {
		h.flashAndRedirect(w, r, "/tools", "err", "Install ZeroTier before managing networks.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := ensureHostZeroTierService(ctx); err != nil {
		h.flashAndRedirect(w, r, "/tools", "err", "ZeroTier service is not ready: "+err.Error())
		return
	}
	output, err := exec.CommandContext(ctx, "sudo", "-n", zeroTierInstallHelper, action, networkID).CombinedOutput()
	if err != nil {
		detail := truncateText(strings.TrimSpace(string(output)), 300)
		if detail == "" {
			detail = err.Error()
		}
		h.log.Error("host ZeroTier action failed", "action", action, "error", err, "output", detail)
		h.flashAndRedirect(w, r, "/tools", "err", "ZeroTier could not "+action+" network "+networkID+": "+detail)
		return
	}
	h.flashAndRedirect(w, r, "/tools", "ok", success+" "+networkID+".")
}

func (h *Handler) ToolsZeroTierStart(w http.ResponseWriter, r *http.Request) {
	if runtime.GOOS != "linux" {
		h.flashAndRedirect(w, r, "/tools", "err", "ZeroTier service management is only available on Linux.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := ensureHostZeroTierService(ctx); err != nil {
		h.flashAndRedirect(w, r, "/tools", "err", "Could not start ZeroTier: "+err.Error())
		return
	}
	h.flashAndRedirect(w, r, "/tools", "ok", "ZeroTier service started.")
}

func (h *Handler) ToolsZeroTierJoin(w http.ResponseWriter, r *http.Request) {
	h.runZeroTierNetworkAction(w, r, "join", "This host joined ZeroTier network")
}

func (h *Handler) ToolsZeroTierLeave(w http.ResponseWriter, r *http.Request) {
	h.runZeroTierNetworkAction(w, r, "leave", "This host left ZeroTier network")
}
