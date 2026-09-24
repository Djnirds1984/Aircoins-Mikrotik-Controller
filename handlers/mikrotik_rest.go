// This file implements the RouterOS v7 REST API as a transport, so a device
// that only exposes the www or www-ssl service can be managed without enabling
// /ip service api. REST is a JSON wrapper around the same console commands the
// binary API sends.
//
// Mapping (RouterOS manual, "Developer Guides > REST API"):
//
//	print          -> GET    /rest/<menu>          (filters as query params)
//	get            -> GET    /rest/<menu>/<id>
//	add            -> PUT    /rest/<menu>          (JSON body)
//	set            -> PATCH  /rest/<menu>/<id>     (JSON body)
//	remove         -> DELETE /rest/<menu>/<id>
//	any other verb -> POST   /rest/<menu>/<verb>   (JSON body, ".id" included)
//
// POST is documented as the universal method for any console command, so a
// rejected CRUD verb is retried in that form before the command fails.

package handlers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// maxRESTBody caps how much of an answer is read, so a misdirected request (an
// HTTP file server, a proxy error page) cannot exhaust a small board's memory.
const maxRESTBody = 8 << 20

// interfacePrintCommand and interfaceMonitorPath are the one command REST
// cannot serve with a plain read: the binary API's "=.sum" operator has no REST
// equivalent, so those counters are collected separately.
const (
	interfacePrintCommand = "/interface/print"
	interfaceMonitorPath  = "/interface/monitor-traffic"
)

// restTransport speaks the RouterOS v7 REST API over HTTP or HTTPS.
type restTransport struct {
	client   *MikrotikClient
	mode     string
	endpoint string
	base     string
	timeout  time.Duration
	http     *http.Client

	// verified flips once the credentials answered, so a session is not
	// re-probed before every command.
	verified bool
}

func newRestTransport(client *MikrotikClient, candidate transportCandidate) *restTransport {
	timeout := client.timeout
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	scheme := "http"
	if candidate.tls {
		scheme = "https"
	}
	endpoint := candidateEndpoint(candidate)
	return &restTransport{
		client:   client,
		mode:     candidate.mode,
		endpoint: endpoint,
		base:     scheme + "://" + endpoint + "/rest",
		timeout:  timeout,
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
					// MikroTik ships a self signed certificate unless the
					// operator installed one, so verification stays opt-in.
					InsecureSkipVerify: !candidate.verify,
				},
				DialContext:         (&net.Dialer{Timeout: timeout}).DialContext,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
			},
		},
	}
}

// Name reports "rest" or "rest-ssl".
func (t *restTransport) Name() string { return t.mode }

// Connect verifies the credentials once per session with a cheap read.
func (t *restTransport) Connect(ctx context.Context) error {
	if t.verified {
		return nil
	}
	if _, err := t.call(ctx, http.MethodGet, "/system/identity", nil, nil, "connect"); err != nil {
		return err
	}
	t.verified = true
	return nil
}

// Reset forgets the session so the next command proves the credentials again.
func (t *restTransport) Reset() {
	t.verified = false
	t.http.CloseIdleConnections()
}

// Close releases pooled sockets.
func (t *restTransport) Close() error {
	t.http.CloseIdleConnections()
	return nil
}

// Run translates one command into the equivalent REST request and executes it.
func (t *restTransport) Run(ctx context.Context, command string, args ...string) (Reply, error) {
	request, err := translateCommand(command, args)
	if err != nil {
		return Reply{}, routerError(t.endpoint, command, err.Error(), ErrRouterDevice, err)
	}

	reply, err := t.call(ctx, request.method, request.path, request.query, request.body, command)
	if err == nil {
		return t.enrichTraffic(ctx, command, args, reply)
	}
	// Menus that refuse the CRUD verb still answer the universal
	// "POST /rest/<menu>/<command>" form described in the manual.
	if request.method != http.MethodPost && errors.Is(err, ErrRouterNoCommand) {
		if fallback, altErr := translateAsCommand(command, args); altErr == nil {
			alt, retryErr := t.call(ctx, http.MethodPost, fallback.path, nil, fallback.body, command)
			if retryErr == nil {
				t.client.log.Debug("routeros rest accepted the command form", "command", command)
				return t.enrichTraffic(ctx, command, args, alt)
			}
		}
	}
	return reply, err
}

// enrichTraffic fills the counters REST cannot return from a plain print. A
// single-interface read merges the rates from /interface/monitor-traffic (the
// "monitor once" pattern); anything else is returned untouched, so a device
// that forbids the command still renders its page.
func (t *restTransport) enrichTraffic(ctx context.Context, command string, args []string, reply Reply) (Reply, error) {
	if command != interfacePrintCommand || !hasSumArg(args) || len(reply.Re) == 0 {
		return reply, nil
	}
	name := strings.TrimSpace(reply.Re[0]["name"])
	if name == "" {
		return reply, nil
	}
	// "once" is a flag in the console command, so it travels as an empty string.
	body := map[string]string{"interface": name, "once": ""}
	monitor, err := t.call(ctx, http.MethodPost, interfaceMonitorPath, nil, body, command)
	if err != nil || len(monitor.Re) == 0 {
		t.client.log.Debug("routeros rest traffic counters unavailable", "interface", name, "error", err)
		return reply, nil
	}
	mergeRESTCounters(reply.Re[0], monitor.Re[0])
	return reply, nil
}

// mergeRESTCounters copies the monitor-traffic counters onto a print row using
// the property names the binary API returns with "=.sum".
func mergeRESTCounters(row, monitor map[string]string) {
	pairs := [][2]string{
		{"rx-byte", "rx-byte"},
		{"tx-byte", "tx-byte"},
		{"rx-packet", "rx-packets-per-second"},
		{"tx-packet", "tx-packets-per-second"},
		{"rx-rate", "rx-bits-per-second"},
		{"tx-rate", "tx-bits-per-second"},
	}
	for _, pair := range pairs {
		if value, ok := monitor[pair[1]]; ok && value != "" {
			row[pair[0]] = value
		}
	}
}

// restRequest is one console command translated into the REST shape.
type restRequest struct {
	method string
	path   string
	query  url.Values
	body   map[string]string
}

// The console verbs that map onto HTTP methods; everything else is a command.
const (
	rosVerbPrint  = "print"
	rosVerbGetAll = "getall"
	rosVerbGet    = "get"
	rosVerbAdd    = "add"
	rosVerbSet    = "set"
	rosVerbRemove = "remove"
)

// translateCommand converts a binary-API style command ("/ip/hotspot/user/set",
// "=.id=*1", "=disabled=yes") into the equivalent REST request.
//
// A print carrying API-only arguments (.id, .sum) is sent in the command form,
// which mirrors the CLI exactly; a plain print uses the idiomatic GET so filters
// and proplist stay query parameters.
func translateCommand(command string, args []string) (restRequest, error) {
	menu, verb, err := splitCommand(command)
	if err != nil {
		return restRequest{}, err
	}
	fields, filters := splitArgs(args)
	id := fields[".id"]
	delete(fields, ".id")
	// ".proplist" only means something to the console: as a GET query parameter
	// RouterOS reads it as a filter on a property that does not exist and answers
	// with nothing, so a print that asks for one has to use the command form.
	_, wantsProplist := fields[".proplist"]
	proplistValue := fields[".proplist"]
	delete(fields, ".proplist")

	switch verb {
	case rosVerbPrint, rosVerbGetAll, rosVerbGet:
		if id != "" || hasSumArg(args) || wantsProplist {
			body := fields
			if id != "" {
				body[".id"] = id
			}
			if wantsProplist {
				body[".proplist"] = proplistValue
			}
			return restRequest{method: http.MethodPost, path: menu + "/" + verb, body: body}, nil
		}
		query := url.Values{}
		for key, value := range fields {
			query.Set(key, value)
		}
		for key, values := range filters {
			for _, value := range values {
				query.Add(key, value)
			}
		}
		return restRequest{method: http.MethodGet, path: menu, query: query}, nil
	case rosVerbAdd:
		return restRequest{method: http.MethodPut, path: menu, body: fields}, nil
	case rosVerbSet:
		if id == "" {
			return restRequest{}, fmt.Errorf("handlers: a routeros set needs an object id")
		}
		return restRequest{method: http.MethodPatch, path: menu + "/" + idPath(id), body: fields}, nil
	case rosVerbRemove:
		if id == "" {
			return restRequest{}, fmt.Errorf("handlers: a routeros remove needs an object id")
		}
		return restRequest{method: http.MethodDelete, path: menu + "/" + idPath(id)}, nil
	default:
		// active/login, active/logout, monitor-traffic and friends are console
		// commands, which POST takes verbatim.
		if id != "" {
			fields[".id"] = id
		}
		return restRequest{method: http.MethodPost, path: menu + "/" + verb, body: fields}, nil
	}
}

// translateAsCommand renders any command in the universal POST form, used when
// a menu rejects the CRUD verb mapping. The path stays relative to the /rest
// base like every other request built here.
func translateAsCommand(command string, args []string) (restRequest, error) {
	menu, verb, err := splitCommand(command)
	if err != nil {
		return restRequest{}, err
	}
	fields, _ := splitArgs(args)
	return restRequest{method: http.MethodPost, path: menu + "/" + verb, body: fields}, nil
}

// splitCommand separates "/ip/hotspot/user/set" into menu and verb.
func splitCommand(command string) (string, string, error) {
	segments := strings.Split(strings.Trim(strings.TrimSpace(command), "/"), "/")
	if len(segments) < 2 || segments[0] == "" || segments[len(segments)-1] == "" {
		return "", "", fmt.Errorf("handlers: cannot translate command %q to REST", command)
	}
	verb := strings.ToLower(segments[len(segments)-1])
	menu := "/" + strings.Join(segments[:len(segments)-1], "/")
	return menu, verb, nil
}

// splitArgs sorts the command arguments into property/value pairs ("=key=value",
// including "=.id=*1" and "=.proplist=...") and query filters ("?key=value").
// Flag words such as "=.sum rx-byte,tx-byte" have no REST equivalent and are
// skipped here; enrichTraffic handles the traffic counters they ask for.
func splitArgs(args []string) (map[string]string, url.Values) {
	fields := map[string]string{}
	filters := url.Values{}
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		switch {
		case strings.HasPrefix(arg, "="):
			key, value, found := strings.Cut(strings.TrimPrefix(arg, "="), "=")
			if !found || key == "" {
				continue
			}
			fields[key] = value
		case strings.HasPrefix(arg, "?"):
			query, err := url.ParseQuery(strings.TrimPrefix(arg, "?"))
			if err != nil {
				continue
			}
			for key, values := range query {
				for _, value := range values {
					filters.Add(key, value)
				}
			}
		}
	}
	return fields, filters
}

// hasSumArg reports whether a command asks for the API-only "sum" operator.
func hasSumArg(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(strings.TrimSpace(arg), "=.sum") {
			return true
		}
	}
	return false
}

// idPath escapes an object id for use as a URL path segment. RouterOS internal
// ids look like "*1A", and the asterisk has to reach the device literally: a
// percent-encoded "%2A" is not recognised, so PATCH and DELETE would miss. A
// name based id may still contain characters that must be escaped.
func idPath(id string) string {
	trimmed := strings.TrimSpace(id)
	encoded := url.PathEscape(trimmed)
	return strings.ReplaceAll(encoded, "%2A", "*")
}

// call performs one HTTP request and converts the answer into a Reply.
func (t *restTransport) call(ctx context.Context, method, path string, query url.Values, body map[string]string, command string) (Reply, error) {
	target := t.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return Reply{}, routerError(t.endpoint, command, "cannot encode the request: "+err.Error(), ErrRouterDevice, err)
		}
		payload = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return Reply{}, routerError(t.endpoint, command, "cannot build the REST request: "+err.Error(), ErrRouterDevice, err)
	}
	request.SetBasicAuth(t.client.router.Username, t.client.router.Password)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	t.client.log.Debug("routeros rest request", "method", method, "path", path, "command", command)
	response, err := t.http.Do(request)
	if err != nil {
		return Reply{}, t.classify(command, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxRESTBody))
	_ = response.Body.Close()
	if readErr != nil {
		return Reply{}, t.classify(command, readErr)
	}
	if response.StatusCode >= 400 {
		return Reply{}, t.classifyStatus(command, response.StatusCode, response.Status, raw)
	}

	reply, err := parseRESTReply(raw)
	if err != nil {
		return Reply{}, routerError(t.endpoint, command, err.Error(), ErrRouterDevice, err)
	}
	return reply, nil
}

// classifyStatus turns a REST error response into a RouterError. RouterOS
// answers with {"error":406,"message":"Not Acceptable","detail":"..."}.
func (t *restTransport) classifyStatus(command string, status int, statusText string, raw []byte) error {
	var payload struct {
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &payload)

	detail := strings.TrimSpace(strings.Join([]string{payload.Detail, payload.Message}, " "))
	if detail == "" {
		detail = strings.TrimSpace(string(raw))
	}
	sentinel := sentinelForStatus(status, detail)

	message := detail
	switch sentinel {
	case ErrRouterAuth:
		message = "the REST user name or password was rejected"
	case ErrRouterPermission:
		message = "the REST account lacks permission for this menu"
	case ErrRouterUnreachable:
		message = "the device answered HTTP " + strconv.Itoa(status)
	}
	if message == "" {
		message = "the device answered HTTP " + strconv.Itoa(status) + " " + statusText
	}
	return routerError(t.endpoint, command, message, sentinel, nil)
}

// sentinelForStatus maps the HTTP status - and the RouterOS prose when there is
// any - onto the shared sentinels, so the UI shows the same hints it shows for
// the binary API.
func sentinelForStatus(status int, detail string) error {
	if sentinel := classifyDeviceMessage(detail); sentinel != ErrRouterDevice {
		return sentinel
	}
	switch {
	case status == http.StatusUnauthorized:
		return ErrRouterAuth
	case status == http.StatusForbidden:
		return ErrRouterPermission
	case status == http.StatusNotFound:
		return ErrRouterNotFound
	case status == http.StatusMethodNotAllowed, status == http.StatusNotAcceptable:
		return ErrRouterNoCommand
	case status >= 500:
		return ErrRouterUnreachable
	default:
		return ErrRouterDevice
	}
}

// classify converts transport level failures - no HTTP answer at all - into the
// sentinels the rest of the controller expects.
func (t *restTransport) classify(command string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return routerError(t.endpoint, command, "the request was cancelled or timed out", ErrRouterTimeout, err)
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return routerError(t.endpoint, command,
			"the HTTPS certificate was rejected: install a trusted one or untick \"Verify certificate\"",
			ErrRouterUnreachable, err)
	}
	if isTimeoutError(err) {
		return routerError(t.endpoint, command, "the device did not answer in time", ErrRouterTimeout, err)
	}
	// A refused dial is reported with a platform specific error number, so the
	// portable test is the operation itself: "dial" covers a closed port, a
	// wrong address and an unresolvable host, all of which mean the same here.
	var dialErr *net.OpError
	if errors.As(err, &dialErr) && dialErr.Op == "dial" {
		return routerError(t.endpoint, command,
			"nothing is listening on that port: enable the www or www-ssl service for REST",
			ErrRouterUnreachable, err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return routerError(t.endpoint, command, "the API connection to the device was lost", ErrRouterUnreachable, err)
	}
	return routerError(t.endpoint, command, err.Error(), ErrRouterDevice, err)
}

// parseRESTReply converts a RouterOS JSON answer into the Reply shape the rest
// of the package already works with. Replies are arrays of objects (one per
// row) or a single object for a create; RouterOS encodes values as strings, but
// numbers and booleans do appear, so every value is normalised to text.
func parseRESTReply(raw []byte) (Reply, error) {
	trimmed := bytes.TrimSpace(raw)
	out := Reply{Done: map[string]string{}}
	if len(trimmed) == 0 {
		return out, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return Reply{}, fmt.Errorf("the device did not answer the REST API (is www or www-ssl enabled?): %w", err)
	}

	switch value := payload.(type) {
	case []any:
		for _, item := range value {
			if row, ok := stringifyRow(item); ok {
				out.Re = append(out.Re, row)
			}
		}
	case map[string]any:
		if row, ok := stringifyRow(value); ok {
			out.Re = append(out.Re, row)
		}
	}
	if len(out.Re) > 0 {
		// A create answers with the new object, which is where the binary API
		// would have reported "!done =ret=<id>".
		out.Done["ret"] = out.Re[0][".id"]
	}
	return out, nil
}

// stringifyRow flattens one JSON object into the string map a Reply row is.
func stringifyRow(item any) (map[string]string, bool) {
	object, ok := item.(map[string]any)
	if !ok {
		return nil, false
	}
	row := make(map[string]string, len(object))
	for key, value := range object {
		row[key] = stringifyRESTValue(value)
	}
	return row, true
}

// stringifyRESTValue renders any JSON scalar the way the binary API would have
// reported it.
func stringifyRESTValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(typed)
	}
}
