// This file adapts the legacy RouterOS binary API (TCP 8728, or 8729 with TLS)
// to the transport interface used by MikrotikClient. The socket, the
// transparent reconnect and the error classification stay on MikrotikClient in
// mikrotik.go; this type only exposes them under the common interface.

package handlers

import "context"

// rosTransport is the legacy binary API transport: the default on every
// RouterOS version, but it needs the "api"/"api-ssl" service enabled on the
// device.
type rosTransport struct {
	client *MikrotikClient
	mode   string
}

func newRosTransport(client *MikrotikClient, candidate transportCandidate) *rosTransport {
	return &rosTransport{client: client, mode: candidate.mode}
}

// Name reports "api" or "api-ssl".
func (t *rosTransport) Name() string { return t.mode }

// Connect dials and logs in unless a live connection is already open. The
// RouterOS binary API authenticates as part of the handshake, so a successful
// Connect also proves the credentials.
func (t *rosTransport) Connect(ctx context.Context) error {
	_, err := t.client.connection(ctx)
	return err
}

// Run sends one command as the API sentence the device expects.
func (t *rosTransport) Run(ctx context.Context, command string, args ...string) (Reply, error) {
	conn, err := t.client.connection(ctx)
	if err != nil {
		return Reply{}, err
	}
	sentences := make([]string, 0, len(args)+1)
	sentences = append(sentences, command)
	sentences = append(sentences, args...)
	reply, err := conn.RunArgsContext(ctx, sentences)
	if err != nil {
		return Reply{}, err
	}
	return toReply(reply), nil
}

// Reset drops the socket so the next command dials a fresh one.
func (t *rosTransport) Reset() { t.client.drop() }

// Close releases the socket.
func (t *rosTransport) Close() error { return t.client.closeConn() }
