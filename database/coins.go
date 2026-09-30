package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Coin lifecycle states.
const (
	// CoinActive means money is in the tally and none of it has been spent on
	// a session yet.
	CoinActive = "active"
	// CoinConnected means the balance was handed to the device.
	CoinConnected = "connected"
	// CoinExpired means the balance sat untouched past the idle window and was
	// released, so the next customer does not inherit it.
	CoinExpired = "expired"
)

// Limits applied to a hardware report. They are deliberately generous for a
// real coin acceptor (which emits a handful of pulses at a time) and tight
// enough that a stuck input line or a misconfigured node cannot mint an
// unbounded balance.
const (
	// MaxCoinPulsesPerReport caps a single /api/coin-pulse body.
	MaxCoinPulsesPerReport = 1000
	// MaxCoinAmountCentsPerReport caps the money a single report may claim.
	MaxCoinAmountCentsPerReport = 100000
	// MaxCoinGrantedSecondsPerReport caps the access time one report may buy.
	MaxCoinGrantedSecondsPerReport = 7 * 24 * 3600
	// MaxCoinEventIDLen bounds the de-duplication token a node may send.
	MaxCoinEventIDLen = 64
	// MaxCoinNodeIDLen bounds the node identifier kept for the audit trail.
	MaxCoinNodeIDLen = 48
)

// CoinCredit is the credit balance of one paying client.
//
// It is a running total, not a wallet row that is deleted on spend: the
// inserted pulses and the resulting access time stay on the record so an
// operator can reconcile the coin box against the session history.
type CoinCredit struct {
	ID int64
	// Subject is the storage key: "mac:<normalized>" or "ip:<address>".
	Subject string
	// MACAddress is the normalised client MAC, empty when the hotspot has not
	// reported one for this client yet.
	MACAddress string
	// RouterID is the device this credit was last seen on, when known.
	RouterID   *int64
	RouterName string
	// NodeID identifies the coin acceptor that fed the credit.
	NodeID string
	// Pulses is the total number of acceptor's pulses received.
	Pulses int
	// AmountCents is the total face value inserted, in the smallest unit of the
	// local currency. It is informational: the granted time is what the node
	// asked for, so an operator can price differently without a schema change.
	AmountCents int64
	// GrantedSeconds is the total access time the pulses have bought.
	GrantedSeconds int
	// UsedSeconds is how much of that has already been authorised.
	UsedSeconds int
	// Status is one of CoinActive, CoinConnected or CoinExpired.
	Status string
	// LastEvent is the most recent de-duplication token, so a NodeMCU that
	// retries a POST over a flaky Wi-Fi link cannot double-credit a coin.
	LastEvent string

	LastPulseAt time.Time
	ConnectedAt *time.Time
	ExpiresAt   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RemainingSeconds is the access time still available.
func (c CoinCredit) RemainingSeconds() int {
	remaining := c.GrantedSeconds - c.UsedSeconds
	if remaining < 0 {
		return 0
	}
	return remaining
}

// HasBalance reports whether anything is left to connect with.
func (c CoinCredit) HasBalance() bool { return c.RemainingSeconds() > 0 }

// CoinPulse is one accepted hardware report.
//
// Seconds is computed by the caller (the HTTP layer owns the money-to-time
// rate) so this package never has to know what a coin is worth.
type CoinPulse struct {
	// Subject identifies the paying client. See CoinCredit.Subject.
	Subject string
	// MACAddress is copied alongside the subject when known, for the audit
	// trail and for the session the credit is later spent on.
	MACAddress string
	// RouterID attributes the credit to a device, when the node knows one.
	RouterID *int64
	// NodeID is the coin acceptor's identifier.
	NodeID string
	// Pulses and AmountCents describe what was inserted.
	Pulses      int
	AmountCents int64
	// Seconds is the access time the pulses buy.
	Seconds int
	// EventID de-duplicates a retried POST. Empty disables the check, which is
	// what a hand-rolled test client wants.
	EventID string
	// IdleTTL releases an untouched balance after this long. Zero keeps it
	// until it is spent.
	IdleTTL time.Duration
}

// CoinStore persists the coin-slot credit tally.
type CoinStore struct{ db *DB }

const coinCreditColumns = `
    c.id, c.subject, c.mac_address, c.router_id, COALESCE(r.name, ''), c.node_id,
    c.pulses, c.amount_cents, c.granted_seconds, c.used_seconds, c.status,
    c.last_event, c.last_pulse_at, c.connected_at, c.expires_at,
    c.created_at, c.updated_at`

const coinCreditFrom = ` FROM coin_credits c LEFT JOIN routers r ON r.id = c.router_id`

// ErrCoinPulseInvalid is wrapped by CoinStore.Credit so the HTTP layer can map
// a bad hardware report onto 400 without parsing the message.
var ErrCoinPulseInvalid = errors.New("database: invalid coin pulse")

// ErrCoinPulseDuplicate is returned by Credit when the report's EventID was
// already recorded, so a NodeMCU retrying after a dropped response does not
// pay for the same coin twice. The returned credit is the unchanged balance.

// Credit applies a hardware report to a client's tally and returns the row as
// it now stands.
//
// The whole update is one statement, so two pulses arriving at the same moment
// (a fast acceptor, or a retry storm from a reconnecting node) can never lose a
// count the way a read-then-write would. The de-duplication rule is a WHERE
// clause on that same statement rather than a separate check: if the node's
// EventID is already the one on the row, the update matches no row and nothing
// is applied. Doing it any other way would leave a window in which a retry that
// arrives during the first one's transaction is counted twice.
func (s *CoinStore) Credit(ctx context.Context, pulse CoinPulse, at time.Time) (CoinCredit, error) {
	subject := strings.TrimSpace(pulse.Subject)
	if subject == "" {
		return CoinCredit{}, fmt.Errorf("%w: a subject is required", ErrCoinPulseInvalid)
	}
	if pulse.Pulses < 0 || pulse.Pulses > MaxCoinPulsesPerReport {
		return CoinCredit{}, fmt.Errorf("%w: pulses must be between 0 and %d",
			ErrCoinPulseInvalid, MaxCoinPulsesPerReport)
	}
	if pulse.AmountCents < 0 || pulse.AmountCents > MaxCoinAmountCentsPerReport {
		return CoinCredit{}, fmt.Errorf("%w: amount_cents must be between 0 and %d",
			ErrCoinPulseInvalid, MaxCoinAmountCentsPerReport)
	}
	if pulse.Seconds < 0 || pulse.Seconds > MaxCoinGrantedSecondsPerReport {
		return CoinCredit{}, fmt.Errorf("%w: seconds must be between 0 and %d",
			ErrCoinPulseInvalid, MaxCoinGrantedSecondsPerReport)
	}

	at = at.UTC().Truncate(time.Second)
	mac := NormalizeMAC(pulse.MACAddress)
	event := truncate(strings.TrimSpace(pulse.EventID), MaxCoinEventIDLen)
	node := truncate(strings.TrimSpace(pulse.NodeID), MaxCoinNodeIDLen)

	var expires any
	if pulse.IdleTTL > 0 {
		expires = stamp(at.Add(pulse.IdleTTL))
	}

	// A returning client whose balance was already spent or expired starts from
	// zero again, which the CASE expressions handle without a prior read.
	res, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO coin_credits (
            subject, mac_address, router_id, node_id, pulses, amount_cents,
            granted_seconds, used_seconds, status, last_event, last_pulse_at,
            expires_at, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, 0, 'active', ?, ?, ?, ?, ?)
        ON CONFLICT(subject) DO UPDATE SET
            mac_address     = CASE WHEN excluded.mac_address <> ''
                                   THEN excluded.mac_address
                                   ELSE coin_credits.mac_address END,
            router_id       = COALESCE(excluded.router_id, coin_credits.router_id),
            node_id         = excluded.node_id,
            pulses          = coin_credits.pulses + excluded.pulses,
            amount_cents    = coin_credits.amount_cents + excluded.amount_cents,
            granted_seconds = CASE WHEN coin_credits.status = 'active'
                THEN coin_credits.granted_seconds + excluded.granted_seconds
                ELSE excluded.granted_seconds END,
            used_seconds    = CASE WHEN coin_credits.status = 'active'
                THEN coin_credits.used_seconds ELSE 0 END,
            status          = 'active',
            connected_at    = NULL,
            last_event      = excluded.last_event,
            last_pulse_at   = excluded.last_pulse_at,
            expires_at      = excluded.expires_at,
            updated_at      = excluded.updated_at
        WHERE excluded.last_event = '' OR coin_credits.last_event <> excluded.last_event`,
		subject, mac, nullableInt64(pulse.RouterID), node,
		maxInt(pulse.Pulses, 0), maxInt64(pulse.AmountCents, 0), maxInt(pulse.Seconds, 0),
		event, stamp(at), expires, stamp(at), stamp(at))
	if err != nil {
		return CoinCredit{}, wrapDBError("apply coin pulse", err)
	}

	credit, err := s.Get(ctx, subject)
	if err != nil {
		return CoinCredit{}, err
	}
	// No row changed means the EventID was already recorded. The balance is
	// already correct (it was never touched), so the caller can be told to stop
	// retrying.
	if affected, affErr := res.RowsAffected(); affErr == nil && affected == 0 {
		return credit, ErrCoinPulseDuplicate
	}
	return credit, nil
}

var ErrCoinPulseDuplicate = errors.New("database: duplicate coin pulse")

// Get loads one credit by subject. ErrNotFound is returned when the client has
// never inserted anything.
func (s *CoinStore) Get(ctx context.Context, subject string) (CoinCredit, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return CoinCredit{}, ErrNotFound
	}
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT`+coinCreditColumns+coinCreditFrom+` WHERE c.subject = ?`, subject)
	credit, err := scanCoinCredit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CoinCredit{}, ErrNotFound
	}
	return credit, err
}

// FindByMAC loads the credit of a client the hotspot identified by MAC, which
// is what lets a client that inserted coins by address pick its balance back
// up once RouterOS starts reporting the hardware address.
func (s *CoinStore) FindByMAC(ctx context.Context, mac string) (CoinCredit, error) {
	normalized := NormalizeMAC(mac)
	if len(normalized) != 12 {
		return CoinCredit{}, ErrNotFound
	}
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT`+coinCreditColumns+coinCreditFrom+
			` WHERE c.mac_address = ? ORDER BY c.updated_at DESC LIMIT 1`, normalized)
	credit, err := scanCoinCredit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return CoinCredit{}, ErrNotFound
	}
	return credit, err
}

// Consume marks up to seconds of a balance as spent and returns the row
// afterwards.
//
// It is the bridge between the coin box and the hotspot: the caller turns the
// returned seconds into a RouterOS user, and only what it actually authorised
// is deducted. Deduction happens after the device call rather than before, so
// a router that refuses the login leaves the customer's money on the balance
// instead of swallowing it. The cap keeps the update from ever pushing
// used_seconds past granted_seconds if two "Connect" taps land together.
func (s *CoinStore) Consume(ctx context.Context, subject string, seconds int, at time.Time) (CoinCredit, error) {
	seconds = maxInt(seconds, 0)
	if seconds == 0 {
		return s.Get(ctx, subject)
	}
	at = at.UTC().Truncate(time.Second)
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE coin_credits
           SET used_seconds = MIN(used_seconds + ?, MAX(granted_seconds, used_seconds)),
               status       = CASE WHEN used_seconds + ? >= granted_seconds
                                   THEN 'connected' ELSE status END,
               connected_at = COALESCE(connected_at, ?),
               updated_at   = ?
         WHERE subject = ?`,
		seconds, seconds, stamp(at), stamp(at), subject)
	if err != nil {
		return CoinCredit{}, wrapDBError("consume coin credit", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return CoinCredit{}, wrapDBError("consume coin credit", err)
	}
	if affected == 0 {
		return CoinCredit{}, ErrNotFound
	}
	return s.Get(ctx, subject)
}

// Claim binds a credit to a device once the customer presses "Connect". The
// router id is stored on the row so the session history can be traced back to
// the coin that paid for it.
func (s *CoinStore) Claim(ctx context.Context, subject string, routerID int64, at time.Time) (CoinCredit, error) {
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE coin_credits
           SET router_id  = COALESCE(?, router_id),
               updated_at = ?
         WHERE subject = ?`,
		nullableInt64(optionalID(routerID)), stamp(at.UTC().Truncate(time.Second)), subject)
	if err != nil {
		return CoinCredit{}, wrapDBError("claim coin credit", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return CoinCredit{}, wrapDBError("claim coin credit", err)
	}
	if affected == 0 {
		return CoinCredit{}, ErrNotFound
	}
	return s.Get(ctx, subject)
}

// ExpireIdle releases balances that were topped up but never connected within
// the window, and returns how many rows it released.
//
// The row already carries its own deadline in expires_at (written when the pulse
// landed), so that is what is compared against "now". The idle argument is only
// the fallback for rows that predate the column, which are judged on their own
// updated_at. Applying the window to both would double-count it and hold a
// customer's coin for twice as long as the portal page promised.
//
// Without this, a customer who walks away from the coin box leaves a balance
// that the next person on the same address would inherit. Driven by the existing
// five minute sweeper.
func (s *CoinStore) ExpireIdle(ctx context.Context, idle time.Duration, at time.Time) (int64, error) {
	at = at.UTC().Truncate(time.Second)
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE coin_credits
           SET status     = 'expired',
               expires_at = COALESCE(expires_at, ?),
               updated_at = ?
         WHERE status = 'active'
           AND ((expires_at IS NOT NULL AND expires_at < ?)
             OR (expires_at IS NULL AND updated_at < ?))`,
		stamp(at), stamp(at), stamp(at), stamp(at.Add(-idle)))
	if err != nil {
		return 0, wrapDBError("expire idle coin credits", err)
	}
	return res.RowsAffected()
}

// CoinLedgerEntry is one row of the operator's coin reconciliation list.
type CoinLedgerEntry struct {
	Credit CoinCredit
	// RouterName is the device the credit was last seen on.
	RouterName string
}

// ListActive returns the balances that still hold unspent time, most recently
// fed first. It backs the coin box troubleshooting view.
func (s *CoinStore) ListActive(ctx context.Context, limit int) ([]CoinLedgerEntry, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT`+coinCreditColumns+coinCreditFrom+
			` WHERE c.status = 'active' AND c.granted_seconds > c.used_seconds
			  ORDER BY c.updated_at DESC LIMIT ?`, normalizedLimit(limit))
	if err != nil {
		return nil, wrapDBError("list coin credits", err)
	}
	defer rows.Close()

	out := make([]CoinLedgerEntry, 0, 16)
	for rows.Next() {
		credit, err := scanCoinCredit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, CoinLedgerEntry{Credit: credit, RouterName: credit.RouterName})
	}
	return out, wrapDBError("list coin credits", rows.Err())
}

func scanCoinCredit(row scanner) (CoinCredit, error) {
	var (
		credit    CoinCredit
		routerID  sql.NullInt64
		lastPulse sql.NullString
		connected sql.NullString
		expires   sql.NullString
		createdAt string
		updatedAt string
	)
	err := row.Scan(&credit.ID, &credit.Subject, &credit.MACAddress, &routerID,
		&credit.RouterName, &credit.NodeID, &credit.Pulses, &credit.AmountCents,
		&credit.GrantedSeconds, &credit.UsedSeconds, &credit.Status,
		&credit.LastEvent, &lastPulse, &connected, &expires, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CoinCredit{}, err
		}
		return CoinCredit{}, wrapDBError("scan coin credit", err)
	}
	if routerID.Valid {
		id := routerID.Int64
		credit.RouterID = &id
	}
	credit.LastPulseAt = parseStamp(lastPulse)
	credit.ConnectedAt = parseStampPtr(connected)
	credit.ExpiresAt = parseStampPtr(expires)
	credit.CreatedAt = parseStamp(sql.NullString{String: createdAt, Valid: createdAt != ""})
	credit.UpdatedAt = parseStamp(sql.NullString{String: updatedAt, Valid: updatedAt != ""})
	return credit, nil
}

// optionalID turns a zero id into a nil pointer so the SQL COALESCE keeps the
// stored value instead of overwriting it with 0.
func optionalID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// CoinSubject is the storage key for a client, built from whichever identifier
// the caller has. The MAC is preferred because it survives a DHCP lease
// change; the address is the fallback for a guest the hotspot has not
// identified yet.
func CoinSubject(mac, ip string) string {
	if normalized := NormalizeMAC(mac); len(normalized) == 12 {
		return "mac:" + normalized
	}
	if address := NormalizeIP(ip); address != "" {
		return "ip:" + address
	}
	return ""
}

// CoinSubjectMAC is the MAC embedded in a subject, or "" when the key is an
// address.
func CoinSubjectMAC(subject string) string {
	rest, found := strings.CutPrefix(subject, "mac:")
	if !found {
		return ""
	}
	return NormalizeMAC(rest)
}

// FormatCoinSeconds renders an access time as "1h 30m" for the portal and the
// operator pages. It reuses the voucher's wording so both read the same.
func FormatCoinSeconds(seconds int) string {
	if seconds <= 0 {
		return "0m"
	}
	return formatMinutes(seconds / 60)
}
