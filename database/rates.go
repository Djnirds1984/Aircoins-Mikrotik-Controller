package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Rate is one pricing tier of the piso Wi-Fi coin slot.
//
// A tier says "N acceptor pulses, worth X pesos, buy D days H hours M minutes".
// Tiers are additive rather than exclusive: an acceptor that emits one pulse per
// coin is configured with a single one-pulse tier, while a machine that emits
// four pulses for the same coin is configured with a four-pulse tier. Several
// tiers can be active at once, which is what lets a 10-peso coin be priced
// differently from a 5-peso one.
//
// The Days/Hours/Minutes triple is what the operator picks in the RATES form. It
// is stored as three columns rather than only as seconds so the form can be
// rendered back with the dropdowns on the values that were actually chosen,
// instead of guessing which combination produced a stored second count.
type Rate struct {
	ID int64
	// Label is a free-text name for the operator's own reference, e.g.
	// "5-peso coin". Optional.
	Label string
	// Pulses is how many acceptor pulses this tier covers. Always >= 1.
	Pulses int
	// AmountCents is what the customer pays, in the smallest currency unit.
	// Informational: it feeds the reconciliation ledger, not the allowance.
	AmountCents int64
	// Days, Hours and Minutes are the structured session time the pulses award.
	Days    int
	Hours   int
	Minutes int
	// GrantedSeconds is the flattened form of the triple above. It is derived
	// on write and never edited directly, so the two can never disagree.
	GrantedSeconds int
	// Active reports whether the tier participates in pricing. A disabled tier
	// keeps its row so a credit issued while it was live can still be
	// explained later.
	Active bool
	// CreatedAt and UpdatedAt are UTC stamps.
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Rate bounds. They mirror the CHECK constraints on the table and exist so the
// store can reject a bad tier with a readable error instead of a constraint
// violation from deep inside the driver.
const (
	// MaxRatePulses caps how many acceptor pulses one tier may cover.
	MaxRatePulses = 1000
	// MaxRateDays is the largest day field the form offers.
	MaxRateDays = 30
	// MaxRateHours is the largest hour field the form offers. Hours and minutes
	// are not cumulative, so a session of a day and a half is entered as
	// 1 day 12 hours rather than as 36 hours.
	MaxRateHours = 23
	// MaxRateMinutes is the largest minute field the form offers.
	MaxRateMinutes = 59
	// MaxRateSeconds caps the flattened allowance of a single tier.
	MaxRateSeconds = MaxRateDays*24*3600 + MaxRateHours*3600 + MaxRateMinutes*60
	// MaxRateLabelRunes bounds the operator's own label.
	MaxRateLabelRunes = 60
	// MaxRateAmountCents caps a single tier's face value.
	MaxRateAmountCents = 100000
)

// ErrRateInvalid is wrapped by the store for any rejected tier, so the HTTP
// layer can map it to 400 without matching on message text.
var ErrRateInvalid = errors.New("database: invalid rate")

// ErrNoActiveRates is returned by Price when no tier can price a pulse. The HTTP
// layer falls back to COIN_SECONDS_PER_PULSE, so an install that has never
// opened the RATES page keeps working exactly as it did.
var ErrNoActiveRates = errors.New("database: no active coin rates")

// RateStore persists the coin-slot pricing tiers.
type RateStore struct{ db *DB }

const rateColumns = `
    r.id, r.label, r.pulses, r.amount_cents, r.days, r.hours, r.minutes,
    r.granted_seconds, r.is_active, r.created_at, r.updated_at`

const rateFrom = ` FROM rates r`

// NormalizeRateTime folds a days/hours/minutes triple into whole seconds.
//
// Each field is bounded on its own, matching the dropdowns the admin form
// renders: 0-30 days, 0-23 hours, 0-59 minutes. The returned triple is the
// re-derived, canonical form of the same duration, so what the form reads back
// after a save always describes the session that is actually stored.
//
// The split is fixed rather than carrying, because the form can only ever
// produce values inside those ranges: there is no UI path that yields "25
// hours", so normalising for one would be handling an input the operator cannot
// give. A field outside its range is rejected rather than clamped, because a
// silently adjusted price is a wrong price.
func NormalizeRateTime(days, hours, minutes int) (totalSeconds, normDays, normHours, normMinutes int, err error) {
	if days < 0 || days > MaxRateDays {
		return 0, 0, 0, 0, fmt.Errorf("%w: days must be between 0 and %d", ErrRateInvalid, MaxRateDays)
	}
	if hours < 0 || hours > MaxRateHours {
		return 0, 0, 0, 0, fmt.Errorf("%w: hours must be between 0 and %d", ErrRateInvalid, MaxRateHours)
	}
	if minutes < 0 || minutes > MaxRateMinutes {
		return 0, 0, 0, 0, fmt.Errorf("%w: minutes must be between 0 and %d", ErrRateInvalid, MaxRateMinutes)
	}
	total := days*24*3600 + hours*3600 + minutes*60
	if total > MaxRateSeconds {
		return 0, 0, 0, 0, fmt.Errorf("%w: the session may not exceed %d days", ErrRateInvalid, MaxRateDays)
	}
	return total, total / 86400, (total % 86400) / 3600, (total % 3600) / 60, nil
}

// validate checks a tier and returns its normalised form, with granted_seconds
// computed. Every write path goes through here, so the structured triple and the
// flattened seconds can never drift apart.
func (r Rate) validate() (Rate, error) {
	out := r
	out.Label = strings.TrimSpace(r.Label)
	if runes := len([]rune(out.Label)); runes > MaxRateLabelRunes {
		return Rate{}, fmt.Errorf("%w: the label must be under %d characters",
			ErrRateInvalid, MaxRateLabelRunes)
	}
	if out.Pulses < 1 || out.Pulses > MaxRatePulses {
		return Rate{}, fmt.Errorf("%w: pulses must be between 1 and %d", ErrRateInvalid, MaxRatePulses)
	}
	if out.AmountCents < 0 || out.AmountCents > MaxRateAmountCents {
		return Rate{}, fmt.Errorf("%w: the price must be between 0 and %d",
			ErrRateInvalid, MaxRateAmountCents)
	}
	total, days, hours, minutes, err := NormalizeRateTime(out.Days, out.Hours, out.Minutes)
	if err != nil {
		return Rate{}, err
	}
	if total <= 0 {
		// A tier that awards no time would silently swallow a customer`s coin.
		// It is rejected here, at configuration time, where an operator can see
		// and fix it, rather than at the coin box.
		return Rate{}, fmt.Errorf("%w: a rate must award some time (set at least one minute)", ErrRateInvalid)
	}
	out.Days, out.Hours, out.Minutes, out.GrantedSeconds = days, hours, minutes, total
	return out, nil
}

// SecondsPerPulse is what one pulse is worth under this tier, rounded down.
//
// It is derived, not stored, and it exists so a node that only knows how to
// count pulses can be told the rate without the tier having to be a
// single-pulse tier.
func (r Rate) SecondsPerPulse() int { return r.GrantedSeconds / r.Pulses }

// CentsPerPulse is the face value of one pulse under this tier.
func (r Rate) CentsPerPulse() int64 { return r.AmountCents / int64(r.Pulses) }

// TimeLabel renders the allowance as "1d 2h 30m", for the panel and the ledger.
func (r Rate) TimeLabel() string {
	parts := make([]string, 0, 3)
	if r.Days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", r.Days))
	}
	if r.Hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", r.Hours))
	}
	if r.Minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", r.Minutes))
	}
	if len(parts) == 0 {
		return "0m"
	}
	return strings.Join(parts, " ")
}

// List returns every tier, active or not, active rows first and then cheapest
// pulse count.
//
// Inactive rows are included deliberately: the RATES page has to show what a
// retired tier was so the operator can understand an old credit, and offering a
// one-click re-enable is how a tier switched off by mistake comes back.
func (s *RateStore) List(ctx context.Context) ([]Rate, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT`+rateColumns+rateFrom+` ORDER BY r.is_active DESC, r.pulses ASC, r.id ASC`)
	if err != nil {
		return nil, wrapDBError("list rates", err)
	}
	defer rows.Close()

	out := make([]Rate, 0, 8)
	for rows.Next() {
		rate, err := scanRate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rate)
	}
	return out, wrapDBError("list rates", rows.Err())
}

// Active returns only the tiers that currently price pulses.
func (s *RateStore) Active(ctx context.Context) ([]Rate, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Rate, 0, len(all))
	for _, rate := range all {
		if rate.Active {
			out = append(out, rate)
		}
	}
	return out, nil
}

// Get reads one tier by id.
func (s *RateStore) Get(ctx context.Context, id int64) (Rate, error) {
	if id <= 0 {
		return Rate{}, ErrNotFound
	}
	row := s.db.sql.QueryRowContext(ctx, `SELECT`+rateColumns+rateFrom+` WHERE r.id = ?`, id)
	rate, err := scanRate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Rate{}, ErrNotFound
	}
	return rate, err
}

// Create stores a new tier and returns it as written.
func (s *RateStore) Create(ctx context.Context, rate Rate) (Rate, error) {
	rate, err := rate.validate()
	if err != nil {
		return Rate{}, err
	}
	at := stamp(now())
	res, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO rates (label, pulses, amount_cents, days, hours, minutes,
                           granted_seconds, is_active, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rate.Label, rate.Pulses, rate.AmountCents, rate.Days, rate.Hours,
		rate.Minutes, rate.GrantedSeconds, boolToInt(rate.Active), at, at)
	if err != nil {
		return Rate{}, wrapDBError("create rate", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Rate{}, wrapDBError("create rate", err)
	}
	return s.Get(ctx, id)
}

// Update replaces a tier editable fields.
//
// granted_seconds is recomputed here rather than taken from the caller: it is a
// derived column, and letting a form post it would give the operator a second,
// hidden way to set the price that bypasses the days/hours/minutes validation.
func (s *RateStore) Update(ctx context.Context, rate Rate) (Rate, error) {
	if rate.ID <= 0 {
		return Rate{}, ErrNotFound
	}
	rate, err := rate.validate()
	if err != nil {
		return Rate{}, err
	}
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE rates
           SET label = ?, pulses = ?, amount_cents = ?, days = ?, hours = ?,
               minutes = ?, granted_seconds = ?, is_active = ?, updated_at = ?
         WHERE id = ?`,
		rate.Label, rate.Pulses, rate.AmountCents, rate.Days, rate.Hours,
		rate.Minutes, rate.GrantedSeconds, boolToInt(rate.Active),
		stamp(now()), rate.ID)
	if err != nil {
		return Rate{}, wrapDBError("update rate", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Rate{}, ErrNotFound
	}
	return s.Get(ctx, rate.ID)
}

// SetActive enables or disables a tier without touching its price, so
// suspending a denomination for a day does not require retyping it.
func (s *RateStore) SetActive(ctx context.Context, id int64, active bool) (Rate, error) {
	if id <= 0 {
		return Rate{}, ErrNotFound
	}
	res, err := s.db.sql.ExecContext(ctx,
		`UPDATE rates SET is_active = ?, updated_at = ? WHERE id = ?`,
		boolToInt(active), stamp(now()), id)
	if err != nil {
		return Rate{}, wrapDBError("set rate active", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Rate{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

// Delete removes a tier outright.
//
// Retiring a tier is SetActive(false), which is what the panel offers for a tier
// that is in use. Delete exists for a typo made during configuration and is
// deliberately the only irreversible action in this store.
func (s *RateStore) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return ErrNotFound
	}
	res, err := s.db.sql.ExecContext(ctx, `DELETE FROM rates WHERE id = ?`, id)
	if err != nil {
		return wrapDBError("delete rate", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRate(row scanner) (Rate, error) {
	var (
		rate      Rate
		active    int
		createdAt string
		updatedAt string
	)
	err := row.Scan(&rate.ID, &rate.Label, &rate.Pulses, &rate.AmountCents,
		&rate.Days, &rate.Hours, &rate.Minutes, &rate.GrantedSeconds,
		&active, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Rate{}, err
		}
		return Rate{}, wrapDBError("scan rate", err)
	}
	rate.Active = active != 0
	rate.CreatedAt = parseStamp(sql.NullString{String: createdAt, Valid: createdAt != ""})
	rate.UpdatedAt = parseStamp(sql.NullString{String: updatedAt, Valid: updatedAt != ""})
	return rate, nil
}

// RateAllocation is the outcome of pricing one hardware report against the
// active tiers.
type RateAllocation struct {
	// Seconds is the access time the pulses bought.
	Seconds int
	// Cents is the face value the pulses are worth, for the ledger.
	Cents int64
	// PulsesConsumed is how many pulses the active tiers explained.
	PulsesConsumed int
	// TierIDs lists the tiers that were used, in the order they were taken. It
	// is logged so a credit can be traced back to the price it was sold at.
	TierIDs []int64
	// Label names the tier when one covered the whole report, which is the
	// common case and the one worth showing in the panel.
	Label string
}

// Price turns a pulse count into an access time by consuming the active tiers.
//
// The tiers are additive and consumed greedily from the largest down, with the
// remainder handled by the smallest tier. That mirrors how a customer actually
// pays: a 20-peso coin that fires four pulses is matched against a four-pulse
// tier before a one-pulse tier is considered, so a bulk coin is never silently
// valued at the single-coin price.
//
// Every pulse is accounted for. When the tiers do not divide the report exactly,
// the leftover pulses are still priced at the smallest active tier rather than
// discarded, because discarding them would hand out less time than the customer
// paid for.
//
// A non-positive pulse count is not an error: it returns an empty allocation,
// which is what a de-duplicated report that carried no pulses needs.
func (s *RateStore) Price(ctx context.Context, pulses int) (RateAllocation, error) {
	if pulses <= 0 {
		return RateAllocation{}, nil
	}

	tiers, err := s.Active(ctx)
	if err != nil {
		return RateAllocation{}, err
	}
	if len(tiers) == 0 {
		return RateAllocation{}, ErrNoActiveRates
	}

	// List orders ascending by pulses, so the last entry is the largest.
	remaining := pulses
	out := RateAllocation{}
	for i := len(tiers) - 1; i >= 0 && remaining > 0; i-- {
		size := tiers[i].Pulses
		if size > remaining {
			continue
		}
		out.Seconds += size * tiers[i].SecondsPerPulse()
		out.Cents += int64(size) * tiers[i].CentsPerPulse()
		out.PulsesConsumed += size
		out.TierIDs = append(out.TierIDs, tiers[i].ID)
		remaining -= size
		if remaining == 0 {
			out.Label = tiers[i].Label
			break
		}
	}

	// Anything the tiers could not cover exactly is priced at the smallest
	// active tier rather than dropped. See the doc comment.
	if remaining > 0 {
		base := tiers[0]
		out.Seconds += remaining * base.SecondsPerPulse()
		out.Cents += int64(remaining) * base.CentsPerPulse()
		out.PulsesConsumed += remaining
	}

	// A tier whose allowance does not divide evenly by its pulse count (a
	// four-pulse tier of one minute gives 15 seconds per pulse) floors to zero.
	// Rounding up to a single second keeps such a tier from being free.
	if out.Seconds < 1 {
		out.Seconds = 1
	}
	return out, nil
}

// FallbackSecondsPerPulse is what a node is told when no tier is configured.
//
// It exists so the node and the panel agree on the "each coin buys X" line even
// on an install that has never opened the RATES page, where the price still comes
// from the COIN_SECONDS_PER_PULSE environment variable.
func (s *RateStore) FallbackSecondsPerPulse(ctx context.Context, fallback int) int {
	tiers, err := s.Active(ctx)
	if err != nil || len(tiers) == 0 {
		return fallback
	}
	if seconds := tiers[0].SecondsPerPulse(); seconds > 0 {
		return seconds
	}
	return fallback
}
