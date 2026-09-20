package routeros

import "context"

// HotspotUser is one row of /ip/hotspot/user/print. Only these fields are
// settable per user on RouterOS 7: password, profile, address, mac-address,
// limit-uptime and the limit-bytes-* trio. Rate limits live on the user profile.
type HotspotUser struct {
	ID              string
	Name            string
	Profile         string
	Server          string
	Address         string
	MACAddress      string
	Email           string
	Uptime          string
	UptimeSeconds   int64
	BytesIn         int64
	BytesOut        int64
	LimitUptime     string
	LimitBytesIn    string
	LimitBytesOut   string
	LimitBytesTotal string
	Disabled        bool
	Dynamic         bool
}

// ActiveSession is one row of /ip/hotspot/active/print.
type ActiveSession struct {
	ID              string
	User            string
	Address         string
	MACAddress      string
	Server          string
	Domain          string
	LoginBy         string
	Uptime          string
	UptimeSeconds   int64
	SessionTimeLeft string
	IdleTime        string
	BytesIn         int64
	BytesOut        int64
	LimitBytesIn    int64
	LimitBytesOut   int64
	LimitBytesTotal int64
}

// HostEntry is one row of /ip/hotspot/host/print. Authorized flips to true once
// the client has logged in, which makes it the most reliable login confirmation
// signal available over the API.
type HostEntry struct {
	ID         string
	MACAddress string
	Address    string
	ToAddress  string
	Server     string
	Authorized bool
	Bypassed   bool
	FoundBy    string
	Uptime     string
	IdleTime   string
	BytesIn    int64
	BytesOut   int64
}

// ReadHotspotUsers runs /ip/hotspot/user/print.
//
// Pass query pairs such as "?name=VOUCHER-1" to filter server side.
func ReadHotspotUsers(ctx context.Context, t Transport, query ...string) ([]HotspotUser, error) {
	args := append([]string{"=.proplist=.id,name,profile,server,address,mac-address,email,uptime,bytes-in,bytes-out,limit-uptime,limit-bytes-in,limit-bytes-out,limit-bytes-total,disabled,dynamic"}, query...)
	reply, err := t.Run(ctx, "/ip/hotspot/user/print", args...)
	if err != nil {
		return nil, err
	}
	out := make([]HotspotUser, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, HotspotUser{
			ID:              row[".id"],
			Name:            row["name"],
			Profile:         row["profile"],
			Server:          row["server"],
			Address:         row["address"],
			MACAddress:      row["mac-address"],
			Email:           row["email"],
			Uptime:          row["uptime"],
			UptimeSeconds:   ParseUptime(row["uptime"]),
			BytesIn:         ParseSize(row["bytes-in"]),
			BytesOut:        ParseSize(row["bytes-out"]),
			LimitUptime:     row["limit-uptime"],
			LimitBytesIn:    row["limit-bytes-in"],
			LimitBytesOut:   row["limit-bytes-out"],
			LimitBytesTotal: row["limit-bytes-total"],
			Disabled:        ParseBool(row["disabled"]),
			Dynamic:         ParseBool(row["dynamic"]),
		})
	}
	return out, nil
}

// ReadHotspotActive runs /ip/hotspot/active/print.
func ReadHotspotActive(ctx context.Context, t Transport, query ...string) ([]ActiveSession, error) {
	args := append([]string{"=.proplist=.id,user,address,mac-address,server,domain,login-by,uptime,session-time-left,idle-time,bytes-in,bytes-out,limit-bytes-in,limit-bytes-out,limit-bytes-total"}, query...)
	reply, err := t.Run(ctx, "/ip/hotspot/active/print", args...)
	if err != nil {
		return nil, err
	}
	out := make([]ActiveSession, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, ActiveSession{
			ID:              row[".id"],
			User:            row["user"],
			Address:         row["address"],
			MACAddress:      row["mac-address"],
			Server:          row["server"],
			Domain:          row["domain"],
			LoginBy:         row["login-by"],
			Uptime:          row["uptime"],
			UptimeSeconds:   ParseUptime(row["uptime"]),
			SessionTimeLeft: row["session-time-left"],
			IdleTime:        row["idle-time"],
			BytesIn:         ParseSize(row["bytes-in"]),
			BytesOut:        ParseSize(row["bytes-out"]),
			LimitBytesIn:    ParseSize(row["limit-bytes-in"]),
			LimitBytesOut:   ParseSize(row["limit-bytes-out"]),
			LimitBytesTotal: ParseSize(row["limit-bytes-total"]),
		})
	}
	return out, nil
}

// ReadHotspotHosts runs /ip/hotspot/host/print.
func ReadHotspotHosts(ctx context.Context, t Transport, query ...string) ([]HostEntry, error) {
	args := append([]string{"=.proplist=.id,mac-address,address,to-address,server,authorized,bypassed,found-by,uptime,idle-time,bytes-in,bytes-out"}, query...)
	reply, err := t.Run(ctx, "/ip/hotspot/host/print", args...)
	if err != nil {
		return nil, err
	}
	out := make([]HostEntry, 0, len(reply.Re))
	for _, row := range reply.Re {
		out = append(out, HostEntry{
			ID:         row[".id"],
			MACAddress: row["mac-address"],
			Address:    row["address"],
			ToAddress:  row["to-address"],
			Server:     row["server"],
			Authorized: ParseBool(row["authorized"]),
			Bypassed:   ParseBool(row["bypassed"]),
			FoundBy:    row["found-by"],
			Uptime:     row["uptime"],
			IdleTime:   row["idle-time"],
			BytesIn:    ParseSize(row["bytes-in"]),
			BytesOut:   ParseSize(row["bytes-out"]),
		})
	}
	return out, nil
}
