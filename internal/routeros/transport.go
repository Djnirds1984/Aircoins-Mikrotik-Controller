package routeros

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	ros "github.com/go-routeros/routeros/v3"
)

// Reply is a normalised RouterOS response: zero or more !re sentences plus the
// terminating !done sentence.
type Reply struct {
	Re   []map[string]string
	Done map[string]string
}

// First returns the first !re sentence or nil.
func (r *Reply) First() map[string]string {
	if r == nil || len(r.Re) == 0 {
		return nil
	}
	return r.Re[0]
}

// Transport runs RouterOS API commands. The rest of this package is written
// against this deliberately narrow interface, which keeps every probe check and
// handler testable without hardware via the faketos package.
type Transport interface {
	// Host identifies the device in logs and error messages.
	Host() string
	// Run executes one command. args are RouterOS words such as "=name=foo",
	// "=.id=*1" or query words like "?name=hsprof1".
	Run(ctx context.Context, command string, args ...string) (*Reply, error)
	// Close releases the connection.
	Close() error
}

// Options configures an API connection.
type Options struct {
	Host     string
	Port     int
	TLS      bool
	Username string
	Password string
	Timeout  time.Duration
}

// Endpoint returns host:port.
func (o Options) Endpoint() string {
	return net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
}

// apiTransport is the real Transport backed by go-routeros.
type apiTransport struct {
	opts Options

	mu     sync.Mutex
	client *ros.Client
}

// Dial connects and authenticates. The returned Transport is safe for
// concurrent use and transparently reconnects after a dropped connection.
func Dial(ctx context.Context, opts Options) (Transport, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	t := &apiTransport{opts: opts}
	if err := t.connect(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// Host implements Transport.
func (t *apiTransport) Host() string { return t.opts.Endpoint() }

func (t *apiTransport) connect(_ context.Context) error {
	address := t.opts.Endpoint()

	var (
		client *ros.Client
		err    error
	)
	if t.opts.TLS {
		// RouterOS ships a self-signed certificate by default, so certificate
		// verification is intentionally relaxed here. The management link is
		// expected to run over a trusted network segment.
		cfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
		client, err = ros.DialTLSTimeout(address, t.opts.Username, t.opts.Password, cfg, t.opts.Timeout)
	} else {
		client, err = ros.DialTimeout(address, t.opts.Username, t.opts.Password, t.opts.Timeout)
	}
	if err != nil {
		return wrapDialError(address, err)
	}

	client.Queue = 32
	t.client = client
	return nil
}

// Run implements Transport.
func (t *apiTransport) Run(ctx context.Context, command string, args ...string) (*Reply, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client == nil {
		if err := t.connect(ctx); err != nil {
			return nil, err
		}
	}

	sentence := make([]string, 0, len(args)+1)
	sentence = append(sentence, command)
	sentence = append(sentence, args...)

	reply, err := t.client.RunArgsContext(ctx, sentence)
	if err != nil {
		// Drop the connection so the next call reconnects cleanly.
		t.client.Close()
		t.client = nil
		return nil, translateError(err)
	}

	return normalise(reply), nil
}

// Close implements Transport.
func (t *apiTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.client == nil {
		return nil
	}
	err := t.client.Close()
	t.client = nil
	return err
}

func normalise(r *ros.Reply) *Reply {
	out := &Reply{Done: map[string]string{}}
	if r == nil {
		return out
	}
	for _, s := range r.Re {
		out.Re = append(out.Re, copyMap(s.Map))
	}
	if r.Done != nil {
		out.Done = copyMap(r.Done.Map)
	}
	return out
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// translateError converts go-routeros errors into classified DeviceErrors.
func translateError(err error) error {
	var devErr *ros.DeviceError
	if errors.As(err, &devErr) {
		msg := devErr.Sentence.Map["message"]
		category := devErr.Sentence.Map["category"]
		return &DeviceError{
			Message:  msg,
			Category: category,
			Sentinel: classifyDevice(msg, category),
			wrapped:  err,
		}
	}
	return err
}
