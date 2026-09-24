package handlers

import (
	"context"
	"strings"
)

const (
	ipPoolMenu     = "/ip/pool"
	bridgeMenu     = "/interface/bridge"
	bridgeVLANMenu = bridgeMenu + "/vlan"
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

// BridgeVLAN is one /interface/bridge/vlan entry. VLANIDs may be one id or a
// RouterOS range, so one table naturally covers individual and ranged VLANs.
type BridgeVLAN struct {
	ID       string
	Bridge   string
	VLANIDs  string
	Tagged   string
	Untagged string
	Current  string
	Disabled bool
}

// BridgeVLANs lists the VLAN forwarding table of every bridge.
func (c *MikrotikClient) BridgeVLANs(ctx context.Context) ([]BridgeVLAN, error) {
	reply, err := c.Run(ctx, bridgeVLANMenu+"/print")
	if err != nil {
		return nil, err
	}
	entries := make([]BridgeVLAN, 0, len(reply.Re))
	for _, row := range reply.Re {
		entries = append(entries, BridgeVLAN{
			ID: row[".id"], Bridge: row["bridge"], VLANIDs: row["vlan-ids"],
			Tagged: row["tagged"], Untagged: row["untagged"],
			Current: row["current-tagged"], Disabled: parseRouterOSBool(row["disabled"]),
		})
	}
	return entries, nil
}

// Bridges lists bridge names, the required owner of a bridge VLAN entry.
func (c *MikrotikClient) Bridges(ctx context.Context) ([]string, error) {
	reply, err := c.Run(ctx, bridgeMenu+"/print", "=.proplist=name")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(reply.Re))
	for _, row := range reply.Re {
		if name := strings.TrimSpace(row["name"]); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// AddBridgeVLAN creates one individual VLAN or a VLAN-ID range on a bridge.
func (c *MikrotikClient) AddBridgeVLAN(ctx context.Context, bridge, vlanIDs, tagged, untagged string) (string, error) {
	args := []string{
		"=bridge=" + strings.TrimSpace(bridge),
		"=vlan-ids=" + strings.TrimSpace(vlanIDs),
	}
	if value := strings.TrimSpace(tagged); value != "" {
		args = append(args, "=tagged="+value)
	}
	if value := strings.TrimSpace(untagged); value != "" {
		args = append(args, "=untagged="+value)
	}
	return c.addROSObject(ctx, bridgeVLANMenu, args)
}
