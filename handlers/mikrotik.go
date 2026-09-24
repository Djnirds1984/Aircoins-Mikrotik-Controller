package handlers

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	ros "github.com/go-routeros/routeros/v3"

	"github.com/djnirds1984/aircoins-mikrotik-controller/database"
)

// Sentinel errors returned by the RouterOS client. Every failure is wrapped in
// a *RouterError that matches one of them, so the HTTP layer can react to the
// cause without parsing device text.
var (
	// ErrRouterUnreachable means the TCP connection to the API port failed or
	// was lost mid command.
	ErrRouterUnreachable = errors.New("routeros: device unreachable")
	// ErrRouterAuth means the API username/password was rejected.
	ErrRouterAuth = errors.New("routeros: authentication failed")
	// ErrRouterTimeout means the device did not answer in time.
	ErrRouterTimeout = errors.New("routeros: device timed out")
	// ErrRouterNoCommand means the RouterOS version does not know the command,
	// usually because the hotspot package is disabled.
	ErrRouterNoCommand = errors.New("routeros: command unavailable on this device")
	// ErrRouterUnknownHost means the hotspot host is not in the device's host
	// table yet, so /ip/hotspot/active/login cannot match a session.
	ErrRouterUnknownHost = errors.New("routeros: client is not known to the hotspot")
	// ErrRouterConflict means the object already exists on the device.
	ErrRouterConflict = errors.New("routeros: object already exists on the device")
	// ErrRouterNotFound means the object does not exist on the device.
	ErrRouterNotFound = errors.New("routeros: object not found on the device")
	// ErrRouterPermission means the API account lacks the required rights.
	ErrRouterPermission = errors.New("routeros: account lacks permission")
	// ErrRouterDevice is the fallback for any other !trap sentence.
	ErrRouterDevice = errors.New("routeros: device reported an error")
)

// RouterError decorates a RouterOS failure with the endpoint and the command
// that produced it, while keeping errors.Is/As working.
type RouterError struct {
	Endpoint string
	Command  string
	Message  string
	Sentinel error

	cause error
}

func (e *RouterError) Error() string {
	switch {
	case e.Endpoint == "":
		return e.Message
	case e.Command == "":
		return fmt.Sprintf("%s at %s", e.Message, e.Endpoint)
	default:
		return fmt.Sprintf("%s at %s (%s)", e.Message, e.Endpoint, e.Command)
	}
}

// Unwrap exposes the underlying transport error.
func (e *RouterError) Unwrap() error { return e.cause }

// Is makes errors.Is(err, ErrRouterAuth) style checks work.
func (e *RouterError) Is(target error) bool {
	if target == nil {
		return false
	}
	if e.Sentinel != nil && errors.Is(e.Sentinel, target) {
		return true
	}
	if e.Sentinel != nil && target == e.Sentinel {
		return true
	}
	return errors.Is(e.cause, target)
}

// routerErrorHint turns a client error into an operator friendly sentence.
func routerErrorHint(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrRouterAuth):
		return "the API username or password was rejected"
	case errors.Is(err, ErrRouterUnreachable):
		return "the router did not answer on the API port (check IP, port, IP service and firewall)"
	case errors.Is(err, ErrRouterTimeout):
		return "the router took too long to answer"
	case errors.Is(err, ErrRouterNoCommand):
		return "the hotspot package is disabled or unsupported on this device"
	case errors.Is(err, ErrRouterUnknownHost):
		return "the client is not in the hotspot host table yet"
	case errors.Is(err, ErrRouterPermission):
		return "the API account needs read/write access to the hotspot menus"
	case errors.Is(err, ErrRouterNotFound):
		return "the object does not exist on the router"
	case errors.Is(err, ErrRouterConflict):
		return "the object already exists on the router"
	default:
		return err.Error()
	}
}

// Reply is a normalised RouterOS answer: the !re rows plus the !done sentence.
type Reply struct {
	Re   []map[string]string
	Done map[string]string
}

// First returns the first row, or nil when there is none.
func (r Reply) First() map[string]string {
	if len(r.Re) == 0 {
		return nil
	}
	return r.Re[0]
}

// ID returns the object id reported by an add command.
func (r Reply) ID() string {
	if r.Done == nil {
		return ""
	}
	return r.Done["ret"]
}

func toReply(reply *ros.Reply) Reply {
	if reply == nil {
		return Reply{}
	}
	out := Reply{Done: map[string]string{}}
	for _, sentence := range reply.Re {
		out.Re = append(out.Re, sentence.Map)
	}
	if reply.Done != nil {
		out.Done = reply.Done.Map
	}
	return out
}

// runAttempts is how many times a command is tried before giving up. A dropped
// API connection is retried once on a fresh connection; anything else fails
// immediately.
const runAttempts = 2

// retryDelay separates the retry from the failed attempt so a rebooting device
// has a moment to accept connections again.
const retryDelay = 350 * time.Millisecond

// MikrotikClient is a reconnecting RouterOS API client bound to one router.
// It is safe for concurrent use: commands are serialised on the single
// connection the way the RouterOS API expects.
type MikrotikClient struct {
	router   database.Router
	endpoint string
	timeout  time.Duration
	log      *slog.Logger

	// tp is the protocol selected for this router (legacy binary API or REST)
	// and transportName is its identifier, used in logs and stored in
	// routers.last_transport.
	tp            transport
	transportName string

	// mu guards the binary API socket, which is kept open and reconnected
	// transparently. The REST transport is stateless and ignores these.
	mu          sync.Mutex
	conn        *ros.Client
	connectedAt time.Time
	reconnects  int
}

// DialRouter connects to a router and verifies the credentials, trying every
// transport candidate the router's mode allows. The client keeps the winning
// transport for all later commands.
func DialRouter(ctx context.Context, router database.Router, timeout time.Duration, logger *slog.Logger) (*MikrotikClient, error) {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	client := &MikrotikClient{
		router:   router,
		endpoint: router.Endpoint(),
		timeout:  timeout,
		log:      logger.With("router", router.Name, "endpoint", router.Endpoint()),
	}

	candidates := transportCandidates(router)
	var lastErr error
	for index, candidate := range candidates {
		// Plain REST must be explicit about its port. In particular, never turn
		// an omitted rest_port into port 80: a public hostname can expose an
		// unrelated web service there and the controller must not send API
		// credentials to it.
		if candidate.mode == database.TransportREST && (candidate.port <= 0 || candidate.port == 80) {
			lastErr = routerError(candidateEndpoint(candidate), "",
				"REST over HTTP requires an explicit web port other than 80; refusing the unsafe port 80", ErrRouterUnreachable, nil)
			break
		}
		tp := newTransport(client, candidate)
		// Point the client - and therefore the dial and every error message - at
		// the candidate being tried, so a failed probe names the right port.
		client.endpoint = candidateEndpoint(candidate)
		attemptCtx, cancel := context.WithTimeout(ctx, candidateTimeout(timeout, len(candidates)))
		err := tp.Connect(attemptCtx)
		cancel()
		if err == nil {
			client.tp = tp
			client.transportName = candidate.mode
			if index > 0 {
				logger.Info("router transport selected", "router", router.Name,
					"transport", candidate.mode, "port", candidate.port)
			}
			return client, nil
		}
		lastErr = err
		_ = tp.Close()
		logger.Debug("router transport unavailable", "router", router.Name,
			"transport", candidate.mode, "port", candidate.port, "error", err)
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = routerError(client.endpoint, "", "this router has no usable transport", ErrRouterUnreachable, nil)
	}
	return nil, lastErr
}

// transport runs one RouterOS command on a device. The legacy binary API and
// the RouterOS v7 REST API both implement it, so every command in this package
// is written once.
type transport interface {
	// Name is the transport identifier stored in routers.last_transport.
	Name() string
	// Connect dials and authenticates. It is idempotent, so Run can call it
	// before every command.
	Connect(ctx context.Context) error
	// Run executes one command and returns its rows.
	Run(ctx context.Context, command string, args ...string) (Reply, error)
	// Reset drops a dead connection so the next command dials again.
	Reset()
	// Close releases the connection.
	Close() error
}

// transportCandidate is one protocol/port pair the controller may try.
type transportCandidate struct {
	mode   string
	host   string
	port   int
	tls    bool
	verify bool
}

// probeCap bounds a single attempt while auto mode probes several transports,
// so an unreachable device cannot stall a page for the sum of every timeout.
const probeCap = 4 * time.Second

// newTransport builds the protocol implementation for one candidate.
func newTransport(client *MikrotikClient, candidate transportCandidate) transport {
	if candidate.mode == database.TransportREST || candidate.mode == database.TransportRESTSsl {
		return newRestTransport(client, candidate)
	}
	return newRosTransport(client, candidate)
}

// transportCandidates lists the protocols to try, in order, for one router.
//
// An explicit mode yields a single candidate. Auto mode tries the secure
// transport that worked last time first, so a healthy router is not probed
// twice, then HTTPS REST and the binary API transports. Plain HTTP REST is
// deliberately excluded: a hostname can expose an unrelated web service on
// port 80, and a remote controller must not send credentials to it by accident.
// Operators who know the www service is the desired endpoint can still select
// REST over HTTP explicitly.
func transportCandidates(router database.Router) []transportCandidate {
	rest := func(tls bool) transportCandidate {
		port := router.RestPort
		if port <= 0 {
			// HTTPS has a safe standard default. Plain HTTP deliberately has
			// none: the operator must name the actual www port (10775, for
			// example) instead of accidentally contacting a public web server
			// on port 80.
			port = 0
			if tls {
				port = 443
			}
		}
		mode := database.TransportREST
		if tls {
			mode = database.TransportRESTSsl
		}
		return transportCandidate{mode: mode, host: router.Host, port: port, tls: tls, verify: router.VerifyTLS}
	}
	api := func(tls bool) transportCandidate {
		port := router.Port
		if port <= 0 {
			port = 8728
			if tls {
				port = 8729
			}
		}
		mode := database.TransportAPI
		if tls {
			mode = database.TransportAPISSL
		}
		return transportCandidate{mode: mode, host: router.Host, port: port, tls: tls, verify: router.VerifyTLS}
	}

	switch router.TransportMode() {
	case database.TransportAPI:
		return []transportCandidate{api(false)}
	case database.TransportAPISSL:
		return []transportCandidate{api(true)}
	case database.TransportREST:
		return []transportCandidate{rest(false)}
	case database.TransportRESTSsl:
		return []transportCandidate{rest(true)}
	}

	// Auto mode is secure-only: never probe an unrelated HTTP service on port 80,
	// even if an older run happened to remember that transport succeeding.
	order := []transportCandidate{rest(true), api(true), api(false)}
	last := router.LastTransport
	if last == database.TransportREST || !database.ValidTransport(last) || last == database.TransportAuto {
		last = ""
	}
	out := make([]transportCandidate, 0, len(order))
	for _, candidate := range order {
		if candidate.mode == last {
			out = append(out, candidate)
		}
	}
	for _, candidate := range order {
		if candidate.mode == last {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

// candidateTimeout shortens each attempt while several candidates are probed; a
// single configured transport gets the full budget.
func candidateTimeout(full time.Duration, candidates int) time.Duration {
	if candidates <= 1 || full <= probeCap {
		return full
	}
	return probeCap
}

// candidateEndpoint renders host:port for logs and error messages.
func candidateEndpoint(candidate transportCandidate) string {
	if candidate.port <= 0 {
		return candidate.host
	}
	return fmt.Sprintf("%s:%d", candidate.host, candidate.port)
}

// routerError builds the decorated error every transport returns.
func routerError(endpoint, command, message string, sentinel, cause error) error {
	return &RouterError{Endpoint: endpoint, Command: command, Message: message, Sentinel: sentinel, cause: cause}
}

// Close releases the connection held by the active transport.
func (c *MikrotikClient) Close() error {
	if c.tp != nil {
		if err := c.tp.Close(); err != nil {
			return err
		}
	}
	return c.closeConn()
}

// closeConn releases the binary API socket. It is idempotent.
func (c *MikrotikClient) closeConn() error {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// TransportName reports the protocol in use: api, api-ssl, rest or rest-ssl.
func (c *MikrotikClient) TransportName() string { return c.transportName }

// Router returns the inventory record this client was built from.
func (c *MikrotikClient) Router() database.Router { return c.router }

// Reconnects reports how many times the connection had to be re-established.
func (c *MikrotikClient) Reconnects() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconnects
}

// connection returns the live API connection, dialling when needed.
func (c *MikrotikClient) connection(ctx context.Context) (*ros.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return c.conn, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, &RouterError{Endpoint: c.endpoint, Message: "request cancelled", Sentinel: ErrRouterTimeout, cause: err}
	}

	timeout := c.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return nil, &RouterError{Endpoint: c.endpoint, Message: "no time left to reach the device", Sentinel: ErrRouterTimeout}
	}

	start := time.Now()
	var (
		conn *ros.Client
		err  error
	)
	if c.router.UseTLS {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: c.router.Host,
			// MikroTik ships a self signed certificate by default; verification
			// is opt-in per router so API-SSL works out of the box.
			InsecureSkipVerify: !c.router.VerifyTLS,
		}
		conn, err = ros.DialTLSTimeout(c.endpoint, c.router.Username, c.router.Password, tlsConfig, timeout)
	} else {
		conn, err = ros.DialTimeout(c.endpoint, c.router.Username, c.router.Password, timeout)
	}
	if err != nil {
		return nil, c.classify("dial", err)
	}
	conn.SetLogHandler(c.log.Handler())
	c.conn = conn
	c.connectedAt = time.Now()
	c.log.Debug("routeros api connected", "dial_ms", time.Since(start).Milliseconds(), "tls", c.router.UseTLS)
	return conn, nil
}

// drop closes the current connection so the next command dials a fresh one.
func (c *MikrotikClient) drop() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.reconnects++
	c.mu.Unlock()

	if conn != nil {
		if err := conn.Close(); err != nil {
			c.log.Debug("closing dropped routeros connection", "error", err)
		}
	}
}

// Run executes one RouterOS command and returns its rows.
//
// A dropped connection is retried once on a fresh connection (this is the
// "router rebooted / WiFi blipped" case); every other failure is classified and
// returned straight away so the UI can explain what happened.
func (c *MikrotikClient) Run(ctx context.Context, command string, args ...string) (Reply, error) {
	var lastErr error
	for attempt := 1; attempt <= runAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, retryDelay); err != nil {
				if lastErr != nil {
					return Reply{}, lastErr
				}
				return Reply{}, err
			}
			c.tp.Reset()
		}

		if err := c.tp.Connect(ctx); err != nil {
			lastErr = err
			if !isConnectionLoss(err) || attempt == runAttempts {
				return Reply{}, err
			}
			c.log.Warn("cannot reach router, retrying", "error", err)
			continue
		}

		reply, err := c.tp.Run(ctx, command, args...)
		if err == nil {
			return reply, nil
		}
		classified := c.classify(command, err)
		if !isConnectionLoss(err) || attempt == runAttempts {
			return Reply{}, classified
		}
		lastErr = classified
		c.log.Warn("routeros connection lost, reconnecting",
			"command", command, "attempt", attempt, "error", err)
	}
	if lastErr == nil {
		lastErr = &RouterError{Endpoint: c.endpoint, Command: command, Message: "command failed", Sentinel: ErrRouterDevice}
	}
	return Reply{}, lastErr
}

// classify converts a transport or device error into a RouterError with a
// sentinel so callers never have to inspect RouterOS prose.
func (c *MikrotikClient) classify(command string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &RouterError{Endpoint: c.endpoint, Command: command,
			Message: "the request was cancelled or timed out", Sentinel: ErrRouterTimeout, cause: err}
	}

	var deviceErr *ros.DeviceError
	if errors.As(err, &deviceErr) {
		message := deviceErr.Error()
		return &RouterError{Endpoint: c.endpoint, Command: command,
			Message: message, Sentinel: classifyDeviceMessage(message), cause: err}
	}
	var unknown *ros.UnknownReplyError
	if errors.As(err, &unknown) {
		return &RouterError{Endpoint: c.endpoint, Command: command,
			Message: unknown.Error(), Sentinel: ErrRouterNoCommand, cause: err}
	}
	if isTimeoutError(err) {
		return &RouterError{Endpoint: c.endpoint, Command: command,
			Message: "the device did not answer in time", Sentinel: ErrRouterTimeout, cause: err}
	}
	if isConnectionLoss(err) {
		return &RouterError{Endpoint: c.endpoint, Command: command,
			Message: "the API connection to the device was lost", Sentinel: ErrRouterUnreachable, cause: err}
	}
	return &RouterError{Endpoint: c.endpoint, Command: command,
		Message: err.Error(), Sentinel: ErrRouterDevice, cause: err}
}

// classifyDeviceMessage maps RouterOS !trap text onto a sentinel.
func classifyDeviceMessage(message string) error {
	lower := strings.ToLower(message)
	switch {
	case containsAny(lower, "cannot log in", "invalid user name or password", "bad username or password"):
		return ErrRouterAuth
	case containsAny(lower, "no such command", "unknown command", "bad command name"):
		return ErrRouterNoCommand
	case containsAny(lower, "unknown host", "no such host", "unknown client"):
		return ErrRouterUnknownHost
	case containsAny(lower, "already exists", "already have", "already present", "duplicate"):
		return ErrRouterConflict
	case containsAny(lower, "no such item", "no such profile", "no such user", "not found", "does not exist"):
		return ErrRouterNotFound
	case containsAny(lower, "not permitted", "permission denied", "not enough permissions", "not allowed"):
		return ErrRouterPermission
	case containsAny(lower, "timeout", "timed out", "timed-out"):
		return ErrRouterTimeout
	default:
		return ErrRouterDevice
	}
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// isConnectionLoss reports whether err means "the socket died", which is the
// only class of failure worth retrying on a fresh connection.
func isConnectionLoss(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	if isTimeoutError(err) {
		return false
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return containsAny(lower,
		"connection reset", "broken pipe", "use of closed network connection", "unexpected eof",
		"connection refused", "no route to host", "network is unreachable")
}

// isTimeoutError reports whether err is a network timeout.
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return containsAny(strings.ToLower(err.Error()), "i/o timeout", "timed out")
}

// sleepCtx waits for d unless the context ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// DeviceInfo is the identity and health snapshot of a router.
type DeviceInfo struct {
	Identity     string
	Version      string
	BoardName    string
	Architecture string
	Uptime       string
	CPULoad      int
	FreeMemory   int64
	TotalMemory  int64
}

// DeviceInfo reads /system/identity/print and /system/resource/print.
func (c *MikrotikClient) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	var info DeviceInfo

	identity, err := c.Run(ctx, "/system/identity/print")
	if err != nil {
		return info, err
	}
	if row := identity.First(); row != nil {
		info.Identity = row["name"]
	}

	resource, err := c.Run(ctx, "/system/resource/print")
	if err != nil {
		return info, err
	}
	if row := resource.First(); row != nil {
		info.Version = row["version"]
		info.BoardName = row["board-name"]
		info.Architecture = row["architecture-name"]
		info.Uptime = row["uptime"]
		info.CPULoad = int(parseInt64(row["cpu-load"]))
		info.FreeMemory = parseInt64(row["free-memory"])
		info.TotalMemory = parseInt64(row["total-memory"])
	}
	return info, nil
}

// InterfaceStats is one entry of /interface/print with statistics.
type InterfaceStats struct {
	ID         string
	Name       string
	Type       string
	CPULoad    int
	MTU        int64
	MACAddress string
	RxBytes    int64
	TxBytes    int64
	RxPackets  int64
	TxPackets  int64
	RxRate     int64
	TxRate     int64
}

// InterfaceList lists the interfaces of a device with traffic statistics.
func (c *MikrotikClient) InterfaceList(ctx context.Context) ([]InterfaceStats, error) {
	reply, err := c.Run(ctx, "/interface/print", "=.proplist=.id,name,type,mtu,mac-address",
		"=.sum rx-byte,tx-byte,rx-packet,tx-packet,rx-rate,tx-rate")
	if err != nil {
		return nil, err
	}
	interfaces := make([]InterfaceStats, 0, len(reply.Re))
	for _, row := range reply.Re {
		interfaces = append(interfaces, InterfaceStats{
			ID:         row[".id"],
			Name:       row["name"],
			Type:       row["type"],
			MTU:        parseInt64(row["mtu"]),
			MACAddress: row["mac-address"],
			RxBytes:    parseInt64(row["rx-byte"]),
			TxBytes:    parseInt64(row["tx-byte"]),
			RxPackets:  parseInt64(row["rx-packet"]),
			TxPackets:  parseInt64(row["tx-packet"]),
			RxRate:     parseInt64(row["rx-rate"]),
			TxRate:     parseInt64(row["tx-rate"]),
		})
	}
	return interfaces, nil
}

// MonitorInterface monitors a specific interface for traffic data.
func (c *MikrotikClient) MonitorInterface(ctx context.Context, id string) (InterfaceStats, error) {
	reply, err := c.Run(ctx, "/interface/print", "=.id="+id,
		"=.proplist=.id,name,type,mtu,mac-address",
		"=.sum rx-byte,tx-byte,rx-packet,tx-packet,rx-rate,tx-rate")
	if err != nil {
		return InterfaceStats{}, err
	}
	if len(reply.Re) == 0 {
		return InterfaceStats{}, ErrRouterNotFound
	}
	row := reply.Re[0]
	return InterfaceStats{
		ID:         row[".id"],
		Name:       row["name"],
		Type:       row["type"],
		MTU:        parseInt64(row["mtu"]),
		MACAddress: row["mac-address"],
		RxBytes:    parseInt64(row["rx-byte"]),
		TxBytes:    parseInt64(row["tx-byte"]),
		RxPackets:  parseInt64(row["rx-packet"]),
		TxPackets:  parseInt64(row["tx-packet"]),
		RxRate:     parseInt64(row["rx-rate"]),
		TxRate:     parseInt64(row["tx-rate"]),
	}, nil
}

// InterfaceTraffic is a single data point for the traffic graph.
type InterfaceTraffic struct {
	Timestamp time.Time
	RxBytes   int64
	TxBytes   int64
	RxRate    int64
	TxRate    int64
}

// InterfaceTrafficHistory holds a series of traffic data points for graphing.
type InterfaceTrafficHistory struct {
	InterfaceID   string
	InterfaceName string
	Points        []InterfaceTraffic
}

// MonitorInterfaceTraffic collects traffic data points for graphing.
func (c *MikrotikClient) MonitorInterfaceTraffic(ctx context.Context, id string, points *InterfaceTrafficHistory) error {
	stats, err := c.MonitorInterface(ctx, id)
	if err != nil {
		return err
	}
	points.InterfaceID = id
	if points.InterfaceName == "" {
		points.InterfaceName = stats.Name
	}
	points.Points = append(points.Points, InterfaceTraffic{
		Timestamp: time.Now(),
		RxBytes:   stats.RxBytes,
		TxBytes:   stats.TxBytes,
		RxRate:    stats.RxRate,
		TxRate:    stats.TxRate,
	})
	// Keep only the last 60 points
	if len(points.Points) > 60 {
		points.Points = points.Points[len(points.Points)-60:]
	}
	return nil
}

// HotspotActive is one entry of /ip/hotspot/active/print.
type HotspotActive struct {
	ID         string
	User       string
	Address    string
	MACAddress string
	Uptime     string
	LoginBy    string
	Server     string
	Comment    string
	BytesIn    int64
	BytesOut   int64
}

// TotalBytes is the traffic used by the client in both directions.
func (a HotspotActive) TotalBytes() int64 { return a.BytesIn + a.BytesOut }

// ActiveHotspotClients lists the clients currently logged into a hotspot.
func (c *MikrotikClient) ActiveHotspotClients(ctx context.Context) ([]HotspotActive, error) {
	reply, err := c.Run(ctx, "/ip/hotspot/active/print")
	if err != nil {
		return nil, err
	}
	clients := make([]HotspotActive, 0, len(reply.Re))
	for _, row := range reply.Re {
		clients = append(clients, HotspotActive{
			ID:         row[".id"],
			User:       row["user"],
			Address:    row["address"],
			MACAddress: database.FormatMAC(row["mac-address"]),
			Uptime:     row["uptime"],
			LoginBy:    row["login-by"],
			Server:     row["server"],
			Comment:    row["comment"],
			BytesIn:    parseInt64(row["bytes-in"]),
			BytesOut:   parseInt64(row["bytes-out"]),
		})
	}
	return clients, nil
}

// DisconnectClient ends a hotspot session. Newer RouterOS versions remove the
// active entry by id; older ones expose a logout command, so both are tried.
func (c *MikrotikClient) DisconnectClient(ctx context.Context, id, user string) error {
	var removeErr error
	if strings.TrimSpace(id) != "" {
		if _, err := c.Run(ctx, "/ip/hotspot/active/remove", "=.id="+id); err == nil {
			return nil
		} else {
			removeErr = err
			if !errors.Is(err, ErrRouterNotFound) && !errors.Is(err, ErrRouterNoCommand) {
				return err
			}
		}
	}
	if strings.TrimSpace(user) == "" {
		if removeErr != nil {
			return removeErr
		}
		return errors.New("handlers: either a session id or a username is required to disconnect a client")
	}
	if _, err := c.Run(ctx, "/ip/hotspot/active/logout", "=user="+user); err != nil {
		if removeErr != nil {
			return removeErr
		}
		return err
	}
	return nil
}

// HotspotLogin authenticates a client through the API, the way the hotspot
// login page would. On RouterOS builds that only match on the MAC address the
// call is retried with the MAC alone.
func (c *MikrotikClient) HotspotLogin(ctx context.Context, user, password, mac, ip string) error {
	args := []string{"=user=" + user, "=password=" + password}
	if address := database.NormalizeIP(ip); address != "" {
		args = append(args, "=ip="+address)
	}
	if hardware := database.FormatMAC(mac); hardware != "" {
		args = append(args, "=mac-address="+hardware)
	}
	_, err := c.Run(ctx, "/ip/hotspot/active/login", args...)
	if err == nil {
		return nil
	}
	if (errors.Is(err, ErrRouterUnknownHost) || errors.Is(err, ErrRouterNoCommand)) && mac != "" {
		c.log.Warn("hotspot active login rejected, retrying with the MAC address only", "error", err)
		if _, retryErr := c.Run(ctx, "/ip/hotspot/active/login",
			"=user="+user, "=password="+password,
			"=mac-address="+database.FormatMAC(mac)); retryErr == nil {
			return nil
		} else {
			return retryErr
		}
	}
	return err
}

// IPBinding is one entry of /ip/hotspot/ip-binding/print.
type IPBinding struct {
	ID         string
	MACAddress string
	Address    string
	ToAddress  string
	Server     string
	Type       string
	Comment    string
	Disabled   bool
}

// Blocked reports whether the binding denies access.
func (b IPBinding) Blocked() bool { return strings.EqualFold(b.Type, "blocked") }

// IPBindings lists the hotspot IP bindings of a device.
func (c *MikrotikClient) IPBindings(ctx context.Context) ([]IPBinding, error) {
	reply, err := c.Run(ctx, "/ip/hotspot/ip-binding/print")
	if err != nil {
		return nil, err
	}
	bindings := make([]IPBinding, 0, len(reply.Re))
	for _, row := range reply.Re {
		bindings = append(bindings, IPBinding{
			ID:         row[".id"],
			MACAddress: database.FormatMAC(row["mac-address"]),
			Address:    row["address"],
			ToAddress:  row["to-address"],
			Server:     row["server"],
			Type:       row["type"],
			Comment:    row["comment"],
			Disabled:   parseRouterOSBool(row["disabled"]),
		})
	}
	return bindings, nil
}

// BlockMAC denies a client access by creating (or reusing) a blocked IP
// binding for its MAC address. It returns the binding id so the UI can offer an
// undo action.
func (c *MikrotikClient) BlockMAC(ctx context.Context, mac, comment string) (string, error) {
	hardware := database.FormatMAC(mac)
	if database.NormalizeMAC(mac) == "" || hardware == "" {
		return "", errors.New("handlers: a MAC address is required to block a client")
	}
	if comment == "" {
		comment = "blocked by aircoins controller"
	}

	// Reuse an existing binding so repeated clicks cannot pile up duplicates.
	if bindings, err := c.IPBindings(ctx); err == nil {
		wanted := database.NormalizeMAC(hardware)
		for _, binding := range bindings {
			if database.NormalizeMAC(binding.MACAddress) != wanted {
				continue
			}
			if binding.Blocked() {
				return binding.ID, nil
			}
			if _, err := c.Run(ctx, "/ip/hotspot/ip-binding/set",
				"=.id="+binding.ID, "=type=blocked", "=comment="+comment); err != nil {
				return "", err
			}
			return binding.ID, nil
		}
	}

	reply, err := c.Run(ctx, "/ip/hotspot/ip-binding/add",
		"=mac-address="+hardware, "=type=blocked", "=comment="+comment)
	if err != nil {
		return "", err
	}
	return reply.ID(), nil
}

// UnblockBinding removes a hotspot IP binding.
func (c *MikrotikClient) UnblockBinding(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("handlers: a binding id is required")
	}
	_, err := c.Run(ctx, "/ip/hotspot/ip-binding/remove", "=.id="+id)
	return err
}

// HotspotProfile is one entry of /ip/hotspot/user/profile/print. These are the
// per user settings a voucher maps onto: how many devices one login may use,
// the speed and volume it gets and which scripts run around it.
type HotspotProfile struct {
	ID          string
	Name        string
	SharedUsers int
	RateLimit   string
	SessionTime string
	IdleTimeout string
	Keepalive   string

	AddressPool       string
	StatusAutorefresh string
	AddMACCookie      bool
	MACCookieTimeout  string
	AddressList       string
	IncomingFilter    string
	OutgoingFilter    string
	IncomingPktMark   string
	OutgoingPktMark   string
	QueueType         string
	ParentQueue       string
	InsertQueueBefore string
	OnLogin           string
	OnLogout          string
	TransparentProxy  bool
	OpenStatusPage    string
	Advertise         bool
	AdvertiseURL      string
	AdvertiseInterval string
	AdvertiseTimeout  string
	// IsDefault marks the built in profile new users fall back to.
	IsDefault bool
}

// SharedUsersLabel renders the concurrent device allowance for the UI.
func (p HotspotProfile) SharedUsersLabel() string {
	if p.SharedUsers <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(p.SharedUsers)
}

// AdvertisedURLs splits the comma separated advertise-url list.
func (p HotspotProfile) AdvertisedURLs() []string { return splitROSList(p.AdvertiseURL) }

// HotspotProfiles lists the hotspot user profiles of a device.
func (c *MikrotikClient) HotspotProfiles(ctx context.Context) ([]HotspotProfile, error) {
	reply, err := c.Run(ctx, "/ip/hotspot/user/profile/print")
	if err != nil {
		return nil, err
	}
	profiles := make([]HotspotProfile, 0, len(reply.Re))
	for _, row := range reply.Re {
		profiles = append(profiles, hotspotProfileFromRow(row))
	}
	return profiles, nil
}

// EnsureHotspotProfile creates the profile when missing and keeps its
// shared-users (device) allowance in sync.
func (c *MikrotikClient) EnsureHotspotProfile(ctx context.Context, name string, sharedUsers int) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	reply, err := c.Run(ctx, "/ip/hotspot/user/profile/print", "?name="+name)
	if err != nil {
		return err
	}
	if row := reply.First(); row != nil {
		if sharedUsers <= 0 || int(parseInt64(row["shared-users"])) == sharedUsers {
			return nil
		}
		_, err := c.Run(ctx, "/ip/hotspot/user/profile/set",
			"=.id="+row[".id"], "=shared-users="+strconv.Itoa(sharedUsers))
		return err
	}

	args := []string{"=name=" + name}
	if sharedUsers > 0 {
		args = append(args, "=shared-users="+strconv.Itoa(sharedUsers))
	}
	_, err = c.Run(ctx, "/ip/hotspot/user/profile/add", args...)
	return err
}

// HotspotUserSpec describes the hotspot user a voucher maps onto.
type HotspotUserSpec struct {
	Name string
	// Password is normally the voucher code itself.
	Password string
	Profile  string
	Comment  string
	// LimitUptimeMinutes becomes RouterOS limit-uptime (0 = unlimited).
	LimitUptimeMinutes int
	// LimitBytesTotal becomes RouterOS limit-bytes-total (0 = unlimited).
	LimitBytesTotal int64
	// DeviceLimit becomes the profile's shared-users value when > 1.
	DeviceLimit int
}

// HotspotUser is one entry of /ip/hotspot/user/print.
type HotspotUser struct {
	ID          string
	Name        string
	Profile     string
	Uptime      string
	BytesIn     int64
	BytesOut    int64
	LimitUptime string
	LimitBytes  string
	Comment     string
	Disabled    bool
}

// FindHotspotUser returns the hotspot user with the given name, or
// ErrRouterNotFound when it does not exist.
func (c *MikrotikClient) FindHotspotUser(ctx context.Context, name string) (HotspotUser, error) {
	reply, err := c.Run(ctx, "/ip/hotspot/user/print", "?name="+name)
	if err != nil {
		return HotspotUser{}, err
	}
	row := reply.First()
	if row == nil {
		return HotspotUser{}, &RouterError{Endpoint: c.endpoint, Command: "/ip/hotspot/user/print",
			Message: fmt.Sprintf("hotspot user %q does not exist", name), Sentinel: ErrRouterNotFound}
	}
	return HotspotUser{
		ID:          row[".id"],
		Name:        row["name"],
		Profile:     row["profile"],
		Uptime:      row["uptime"],
		BytesIn:     parseInt64(row["bytes-in"]),
		BytesOut:    parseInt64(row["bytes-out"]),
		LimitUptime: row["limit-uptime"],
		LimitBytes:  row["limit-bytes-total"],
		Comment:     row["comment"],
		Disabled:    parseRouterOSBool(row["disabled"]),
	}, nil
}

// EnsureHotspotUser creates or updates the hotspot user a voucher belongs to,
// so the key works even when this controller is offline. It reports whether the
// user had to be created.
//
// When the device does not have the requested profile yet, the profile is
// created and the insert retried: that is what makes the voucher engine self
// healing on a freshly configured router.
func (c *MikrotikClient) EnsureHotspotUser(ctx context.Context, spec HotspotUserSpec) (bool, error) {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return false, errors.New("handlers: hotspot user name is required")
	}

	args := []string{"=name=" + spec.Name}
	if spec.Password != "" {
		args = append(args, "=password="+spec.Password)
	}
	if spec.Profile != "" {
		args = append(args, "=profile="+spec.Profile)
	}
	if spec.LimitUptimeMinutes > 0 {
		args = append(args, "=limit-uptime="+rosDuration(time.Duration(spec.LimitUptimeMinutes)*time.Minute))
	}
	if spec.LimitBytesTotal > 0 {
		args = append(args, "=limit-bytes-total="+strconv.FormatInt(spec.LimitBytesTotal, 10))
	}
	if spec.Comment != "" {
		args = append(args, "=comment="+spec.Comment)
	}
	args = append(args, "=disabled=no")

	existing, err := c.FindHotspotUser(ctx, spec.Name)
	switch {
	case err == nil:
		// Keep the object but refresh its profile and allowances.
		update := append([]string{"=.id=" + existing.ID}, args[1:]...)
		if _, err := c.Run(ctx, "/ip/hotspot/user/set", update...); err != nil {
			return false, err
		}
		return false, nil
	case !errors.Is(err, ErrRouterNotFound):
		return false, err
	}

	if spec.DeviceLimit > 1 && spec.Profile != "" {
		if err := c.EnsureHotspotProfile(ctx, spec.Profile, spec.DeviceLimit); err != nil {
			return false, err
		}
	}

	if _, err := c.Run(ctx, "/ip/hotspot/user/add", args...); err != nil {
		if errors.Is(err, ErrRouterNotFound) && spec.Profile != "" {
			// The profile was missing after all; create it and try once more.
			if profErr := c.EnsureHotspotProfile(ctx, spec.Profile, spec.DeviceLimit); profErr != nil {
				return false, profErr
			}
			if _, retryErr := c.Run(ctx, "/ip/hotspot/user/add", args...); retryErr != nil {
				return false, retryErr
			}
			return true, nil
		}
		return false, err
	}
	return true, nil
}

// RemoveHotspotUser deletes a hotspot user, ignoring an already missing user.
func (c *MikrotikClient) RemoveHotspotUser(ctx context.Context, name string) error {
	user, err := c.FindHotspotUser(ctx, name)
	if errors.Is(err, ErrRouterNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = c.Run(ctx, "/ip/hotspot/user/remove", "=.id="+user.ID)
	return err
}

// rosDuration renders a Go duration the way RouterOS accepts limit-uptime
// values, for example "2d4h30m".
func rosDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	total := int64(d.Seconds())
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60

	var b strings.Builder
	if days > 0 {
		fmt.Fprintf(&b, "%dd", days)
	}
	if hours > 0 {
		fmt.Fprintf(&b, "%dh", hours)
	}
	if minutes > 0 {
		fmt.Fprintf(&b, "%dm", minutes)
	}
	if seconds > 0 && days == 0 && hours == 0 {
		fmt.Fprintf(&b, "%ds", seconds)
	}
	if b.Len() == 0 {
		return "1m"
	}
	return b.String()
}

// parseInt64 parses RouterOS counters, tolerating empty values and unit
// suffixes such as "1.5MiB".
func parseInt64(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return n
	}
	lower := strings.ToLower(value)
	for _, unit := range []struct {
		suffix string
		scale  float64
	}{
		{"kib", 1024}, {"mib", 1024 * 1024}, {"gib", 1024 * 1024 * 1024},
		{"tib", 1024 * 1024 * 1024 * 1024},
	} {
		if !strings.HasSuffix(lower, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(lower, unit.suffix))
		if f, err := strconv.ParseFloat(number, 64); err == nil {
			return int64(f * unit.scale)
		}
	}
	return 0
}

// parseRouterOSBool reads the "true"/"false"/"yes"/"no" flags RouterOS uses.
func parseRouterOSBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "enabled":
		return true
	default:
		return false
	}
}

// dialRouter connects to an inventory router and records the outcome on the
// router row, so the dashboard always shows the last known health.
func (h *Handler) dialRouter(ctx context.Context, router database.Router) (*MikrotikClient, error) {
	start := time.Now()
	client, err := DialRouter(ctx, router, h.cfg.APITimeout, h.log)
	latency := time.Since(start)
	if err != nil {
		h.log.Warn("router api connection failed", "router", router.Name,
			"endpoint", router.Endpoint(), "hint", routerErrorHint(err), "error", err)
		if recErr := h.db.Routers().RecordStatus(ctx, router.ID, database.RouterStatusOffline,
			routerErrorHint(err), latency); recErr != nil {
			h.log.Error("cannot record router status", "router", router.Name, "error", recErr)
		}
		return nil, err
	}
	if recErr := h.db.Routers().RecordStatus(ctx, router.ID, database.RouterStatusOnline, "", latency); recErr != nil {
		h.log.Error("cannot record router status", "router", router.Name, "error", recErr)
	}
	// Remember the protocol that answered so auto mode starts with it next time
	// instead of probing REST and then the binary API on every page load.
	if name := client.TransportName(); name != "" && name != router.LastTransport {
		if recErr := h.db.Routers().RecordTransport(ctx, router.ID, name); recErr != nil {
			h.log.Error("cannot record router transport", "router", router.Name, "error", recErr)
		}
	}
	return client, nil
}
