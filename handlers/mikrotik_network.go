package handlers

import (
	"context"
	"strconv"
	"strings"
)

const (
	ipPoolMenu = "/ip/pool"
	vlanMenu   = "/interface/vlan"
)

// IPPool is one entry of /ip/pool/print. Ranges keeps RouterOS's native
// comma-separated range representation (for example 10.0.0.10-10.0.0.200).
type IPPool struct {
	ID       string
	Name     string
	Ranges   string
	NextPool string
	Comment  string
}

// IPPools lists all IP pools configured on a device.
func (c *MikrotikClient) IPPools(ctx context.Context) ([]IPPool, error) {
	reply, err := c.Run(ctx, ipPoolMenu+"/print")
	if err != nil {
		return nil, err
	}
	pools := make([]IPPool, 0, len(reply.Re))
	for _, row := range reply.Re {
		pools = append(pools, IPPool{
			ID: row[".id"], Name: row["name"], Ranges: row["ranges"],
			NextPool: row["next-pool"], Comment: row["comment"],
		})
	}
	return pools, nil
}

// AddIPPool creates an /ip/pool entry.
func (c *MikrotikClient) AddIPPool(ctx context.Context, name, ranges, nextPool, comment string) (string, error) {
	args := []string{"=ranges=" + strings.TrimSpace(ranges)}
	if value := strings.TrimSpace(name); value != "" {
		args = append(args, "=name="+value)
	}
	if value := strings.TrimSpace(nextPool); value != "" {
		args = append(args, "=next-pool="+value)
	}
	if value := strings.TrimSpace(comment); value != "" {
		args = append(args, "=comment="+value)
	}
	return c.addROSObject(ctx, ipPoolMenu, args)
}

// InterfaceVLAN is one /interface/vlan entry: a VLAN interface that tags a
// single VLAN ID on one parent interface, a bridge or a physical port.
type InterfaceVLAN struct {
	ID        string
	Name      string
	Interface string
	VLANID    string
	MTU       string
	Comment   string
	Running   bool
	Disabled  bool
}

// InterfaceVLANs lists the VLAN interfaces configured on a device.
func (c *MikrotikClient) InterfaceVLANs(ctx context.Context) ([]InterfaceVLAN, error) {
	reply, err := c.Run(ctx, vlanMenu+"/print")
	if err != nil {
		return nil, err
	}
	entries := make([]InterfaceVLAN, 0, len(reply.Re))
	for _, row := range reply.Re {
		entries = append(entries, InterfaceVLAN{
			ID: row[".id"], Name: row["name"], Interface: row["interface"],
			VLANID: row["vlan-id"], MTU: row["mtu"], Comment: row["comment"],
			Running:  parseRouterOSBool(row["running"]),
			Disabled: parseRouterOSBool(row["disabled"]),
		})
	}
	return entries, nil
}

// AddInterfaceVLAN creates one /interface/vlan entry, tagging a single VLAN ID
// on the given parent interface. The parent is any interface of the device, a
// bridge (bridge1) or a physical port (ether5); ranges are never sent, because
// every VLAN an operator wants becomes one interface of its own.
func (c *MikrotikClient) AddInterfaceVLAN(ctx context.Context, name, parent string, vlanID int, mtu, comment string) (string, error) {
	args := []string{
		"=name=" + strings.TrimSpace(name),
		"=interface=" + strings.TrimSpace(parent),
		"=vlan-id=" + strconv.Itoa(vlanID),
	}
	if value := strings.TrimSpace(mtu); value != "" {
		args = append(args, "=mtu="+value)
	}
	if value := strings.TrimSpace(comment); value != "" {
		args = append(args, "=comment="+value)
	}
	return c.addROSObject(ctx, vlanMenu, args)
}
