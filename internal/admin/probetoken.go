package admin

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/crypto"
	"github.com/djnirds1984/aircoins-mikrotik-controller/internal/domain"
)

// probeTokenTTL bounds how long a passing connection test authorises a save.
const probeTokenTTL = 15 * time.Minute

// ErrProbeToken covers every way a probe token can be unacceptable.
var ErrProbeToken = errors.New("the connection test does not cover this router address, or it expired. Run Test connection again")

// issueProbeToken signs the exact endpoint that was probed.
func (s *Server) issueProbeToken(creds domain.RouterCredentials) string {
	payload := probeTokenPayload(creds, time.Now().UTC())
	return payload + "|" + crypto.Sign(s.key, payload)
}

// probeTokenPayload binds host, port, TLS, username and time.
func probeTokenPayload(creds domain.RouterCredentials, at time.Time) string {
	return fmt.Sprintf("%s|%d|%t|%s|%d",
		strings.ToLower(strings.TrimSpace(creds.Host)),
		creds.Port,
		creds.TLS,
		creds.User,
		at.Unix(),
	)
}

// verifyProbeToken checks the signature, the endpoint binding and the age. This
// is what stops a router being tested at one address and saved at another.
func (s *Server) verifyProbeToken(token string, creds domain.RouterCredentials) error {
	if strings.TrimSpace(token) == "" {
		return ErrProbeToken
	}

	idx := strings.LastIndex(token, "|")
	if idx <= 0 {
		return ErrProbeToken
	}
	payload, tag := token[:idx], token[idx+1:]

	if !crypto.Verify(s.key, payload, tag) {
		return ErrProbeToken
	}

	parts := strings.Split(payload, "|")
	if len(parts) != 5 {
		return ErrProbeToken
	}

	// Compare against the credentials being saved.
	want := strings.Split(probeTokenPayload(creds, time.Time{}), "|")
	for i := 0; i < 4; i++ {
		if parts[i] != want[i] {
			return ErrProbeToken
		}
	}

	var issuedUnix int64
	if _, err := fmt.Sscanf(parts[4], "%d", &issuedUnix); err != nil {
		return ErrProbeToken
	}
	issued := time.Unix(issuedUnix, 0).UTC()
	if time.Since(issued) > probeTokenTTL {
		return ErrProbeToken
	}
	return nil
}

// issueProbeTokenFor is the convenience form used by handlers.
func (s *Server) issueProbeTokenFor(host string, port int, tls bool, user string) string {
	return s.issueProbeToken(domain.RouterCredentials{
		Host: host,
		Port: port,
		TLS:  tls,
		User: user,
	})
}
