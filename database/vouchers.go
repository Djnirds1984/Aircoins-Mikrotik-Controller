package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// VoucherStatus is the lifecycle state of a prepaid voucher.
type VoucherStatus string

const (
	// VoucherUnused means the key has never been redeemed.
	VoucherUnused VoucherStatus = "unused"
	// VoucherActive means the key was redeemed and can still be used again.
	VoucherActive VoucherStatus = "active"
	// VoucherUsed means every allowed redemption was consumed.
	VoucherUsed VoucherStatus = "used"
	// VoucherExpired means the validity window lapsed untouched.
	VoucherExpired VoucherStatus = "expired"
	// VoucherDisabled means an operator blocked the key.
	VoucherDisabled VoucherStatus = "disabled"
)

// ErrVoucherNotRedeemable is wrapped by Voucher.Redeemable so callers can test
// for it with errors.Is and still show the human readable reason.
var ErrVoucherNotRedeemable = errors.New("voucher cannot be redeemed")

// Valid reports whether s is a known status.
func (s VoucherStatus) Valid() bool {
	switch s {
	case VoucherUnused, VoucherActive, VoucherUsed, VoucherExpired, VoucherDisabled:
		return true
	default:
		return false
	}
}

// Label is the human readable status name.
func (s VoucherStatus) Label() string {
	switch s {
	case VoucherUnused:
		return "Unused"
	case VoucherActive:
		return "Active"
	case VoucherUsed:
		return "Used"
	case VoucherExpired:
		return "Expired"
	case VoucherDisabled:
		return "Disabled"
	default:
		return string(s)
	}
}

// Class maps the status onto a badge CSS class.
func (s VoucherStatus) Class() string {
	switch s {
	case VoucherUnused:
		return "badge blue"
	case VoucherActive:
		return "badge green"
	case VoucherUsed:
		return "badge slate"
	case VoucherExpired:
		return "badge amber"
	case VoucherDisabled:
		return "badge red"
	default:
		return "badge slate"
	}
}

// Voucher is a prepaid hotspot key.
type Voucher struct {
	ID       int64
	Code     string
	Batch    string
	RouterID *int64
	// RouterName is filled by the store through a LEFT JOIN.
	RouterName string
	// Profile is the hotspot user-profile the key is provisioned with.
	Profile         string
	DurationMinutes int
	DataLimitMB     int
	DeviceLimit     int
	PriceCents      int64
	Status          VoucherStatus
	Uses            int
	MaxUses         int
	Note            string

	CreatedAt   time.Time
	PushedAt    *time.Time
	ActivatedAt *time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
}

// Duration is the session time allowance.
func (v Voucher) Duration() time.Duration {
	return time.Duration(v.DurationMinutes) * time.Minute
}

// DataLimitBytes is the traffic allowance in bytes (RouterOS limit-bytes-total).
func (v Voucher) DataLimitBytes() int64 {
	return int64(v.DataLimitMB) * 1024 * 1024
}

// Price renders the stored cents as a decimal amount.
func (v Voucher) Price() float64 { return float64(v.PriceCents) / 100 }

// RouterIDValue returns 0 when the voucher is not bound to a device.
func (v Voucher) RouterIDValue() int64 {
	if v.RouterID == nil {
		return 0
	}
	return *v.RouterID
}

// RemainingUses is how many redemptions are left.
func (v Voucher) RemainingUses() int {
	if v.MaxUses <= 0 {
		return 0
	}
	if v.Uses >= v.MaxUses {
		return 0
	}
	return v.MaxUses - v.Uses
}

// ExpiredAt reports whether the validity window ended by at.
func (v Voucher) ExpiredAt(at time.Time) bool {
	return v.ExpiresAt != nil && !v.ExpiresAt.After(at)
}

// Redeemable validates the key against the local ledger. Device side limits
// (uptime, bytes, shared users) are enforced by RouterOS itself, so this only
// covers the controller's own bookkeeping.
func (v Voucher) Redeemable(at time.Time) error {
	switch v.Status {
	case VoucherDisabled:
		return fmt.Errorf("%w: this voucher has been disabled", ErrVoucherNotRedeemable)
	case VoucherExpired:
		return fmt.Errorf("%w: this voucher expired on %s", ErrVoucherNotRedeemable, v.ExpiresAt.Format("02 Jan 2006 15:04"))
	case VoucherUsed:
		return fmt.Errorf("%w: this voucher has already been used", ErrVoucherNotRedeemable)
	}
	if v.ExpiredAt(at) {
		return fmt.Errorf("%w: this voucher expired on %s", ErrVoucherNotRedeemable, v.ExpiresAt.Format("02 Jan 2006 15:04"))
	}
	if v.MaxUses > 0 && v.Uses >= v.MaxUses {
		return fmt.Errorf("%w: this voucher has no redemptions left", ErrVoucherNotRedeemable)
	}
	return nil
}

// LimitSummary is a short human readable description of the allowances used on
// the portal success screen and in the voucher table.
func (v Voucher) LimitSummary() string {
	parts := make([]string, 0, 3)
	if v.DurationMinutes > 0 {
		parts = append(parts, formatMinutes(v.DurationMinutes))
	} else {
		parts = append(parts, "unlimited time")
	}
	if v.DataLimitMB > 0 {
		parts = append(parts, fmt.Sprintf("%d MB", v.DataLimitMB))
	} else {
		parts = append(parts, "unlimited data")
	}
	if v.DeviceLimit > 1 {
		parts = append(parts, fmt.Sprintf("%d devices", v.DeviceLimit))
	}
	return strings.Join(parts, " · ")
}

func formatMinutes(minutes int) string {
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	hours := minutes / 60
	mins := minutes % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// ErrDuplicateVoucherCode is returned when a generated code already exists.
var ErrDuplicateVoucherCode = errors.New("database: duplicate voucher code")

// VoucherStore persists prepaid vouchers.
type VoucherStore struct{ db *DB }

const voucherColumns = `
    v.id, v.code, v.batch, v.router_id, COALESCE(r.name, ''), v.profile,
    v.duration_minutes, v.data_limit_mb, v.device_limit, v.price_cents, v.status,
    v.uses, v.max_uses, v.note, v.created_at, v.pushed_at, v.activated_at,
    v.expires_at, v.last_used_at`

const voucherFrom = ` FROM vouchers v LEFT JOIN routers r ON r.id = v.router_id`

// VoucherFilter narrows a voucher listing.
type VoucherFilter struct {
	// Status filters on a single lifecycle state.
	Status VoucherStatus
	// RouterID filters on the bound device.
	RouterID int64
	// Batch filters on the generation batch name.
	Batch string
	// Query matches the code, batch or note (dash/space insensitive).
	Query string
	// Limit caps the number of rows; defaults to 200.
	Limit int
	// Offset skips rows for paging.
	Offset int
}

// CreateBatch inserts vouchers in a single transaction. Codes must already be
// unique; a clash returns ErrDuplicateVoucherCode so the caller can retry the
// whole batch with fresh randomness.
func (s *VoucherStore) CreateBatch(ctx context.Context, vouchers []Voucher) (int, error) {
	if len(vouchers) == 0 {
		return 0, nil
	}
	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapDBError("begin voucher batch", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a harmless no-op

	stmt, err := tx.PrepareContext(ctx, `
        INSERT INTO vouchers (
            code, batch, router_id, profile, duration_minutes, data_limit_mb,
            device_limit, price_cents, status, uses, max_uses, note, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, wrapDBError("prepare voucher insert", err)
	}
	defer stmt.Close()

	created := now()
	inserted := 0
	for _, v := range vouchers {
		code := FormatVoucherCode(v.Code)
		if code == "" {
			return inserted, errors.New("database: voucher code is required")
		}
		status := v.Status
		if status == "" {
			status = VoucherUnused
		}
		maxUses := v.MaxUses
		if maxUses <= 0 {
			maxUses = 1
		}
		if _, err := stmt.ExecContext(ctx,
			code, v.Batch, nullableInt64(v.RouterID), defaultString(v.Profile, "default"),
			maxInt(v.DurationMinutes, 0), maxInt(v.DataLimitMB, 0), maxInt(v.DeviceLimit, 1),
			maxInt64(v.PriceCents, 0), string(status), 0, maxUses, v.Note, stamp(created)); err != nil {
			if isUniqueViolation(err) {
				return inserted, fmt.Errorf("%w: %s", ErrDuplicateVoucherCode, code)
			}
			return inserted, wrapDBError("insert voucher", err)
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return inserted, wrapDBError("commit voucher batch", err)
	}
	return inserted, nil
}

// Get loads one voucher by id.
func (s *VoucherStore) Get(ctx context.Context, id int64) (Voucher, error) {
	row := s.db.sql.QueryRowContext(ctx, `SELECT`+voucherColumns+voucherFrom+` WHERE v.id = ?`, id)
	voucher, err := scanVoucher(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Voucher{}, ErrNotFound
	}
	return voucher, err
}

// FindByCode loads a voucher using the dash/space insensitive lookup key, so
// portal input like "air 4F7K 92QX" resolves correctly.
func (s *VoucherStore) FindByCode(ctx context.Context, code string) (Voucher, error) {
	key := VoucherLookupKey(code)
	if key == "" {
		return Voucher{}, ErrNotFound
	}
	row := s.db.sql.QueryRowContext(ctx,
		`SELECT`+voucherColumns+voucherFrom+` WHERE `+voucherCodeExpr+` = ?`, key)
	voucher, err := scanVoucher(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Voucher{}, ErrNotFound
	}
	return voucher, err
}

// List returns vouchers matching the filter, newest first.
func (s *VoucherStore) List(ctx context.Context, filter VoucherFilter) ([]Voucher, error) {
	var (
		where []string
		args  []any
	)
	if filter.Status != "" {
		where = append(where, "v.status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.RouterID > 0 {
		where = append(where, "v.router_id = ?")
		args = append(args, filter.RouterID)
	}
	if filter.Batch != "" {
		where = append(where, "v.batch = ?")
		args = append(args, filter.Batch)
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToUpper(q) + "%"
		where = append(where, `(`+voucherCodeExpr+` LIKE ? OR upper(v.batch) LIKE ? OR upper(v.note) LIKE ?)`)
		args = append(args, like, like, like)
	}

	query := `SELECT` + voucherColumns + voucherFrom
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY v.id DESC LIMIT ? OFFSET ?"
	args = append(args, normalizedLimit(filter.Limit), maxInt(filter.Offset, 0))

	rows, err := s.db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapDBError("list vouchers", err)
	}
	defer rows.Close()

	var out []Voucher
	for rows.Next() {
		voucher, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, voucher)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("list vouchers", err)
	}
	return out, nil
}

// Count returns how many vouchers match the filter.
func (s *VoucherStore) Count(ctx context.Context, filter VoucherFilter) (int64, error) {
	var (
		where []string
		args  []any
	)
	if filter.Status != "" {
		where = append(where, "v.status = ?")
		args = append(args, string(filter.Status))
	}
	if filter.RouterID > 0 {
		where = append(where, "v.router_id = ?")
		args = append(args, filter.RouterID)
	}
	if filter.Batch != "" {
		where = append(where, "v.batch = ?")
		args = append(args, filter.Batch)
	}
	if q := strings.TrimSpace(filter.Query); q != "" {
		like := "%" + strings.ToUpper(q) + "%"
		where = append(where, `(`+voucherCodeExpr+` LIKE ? OR upper(v.batch) LIKE ? OR upper(v.note) LIKE ?)`)
		args = append(args, like, like, like)
	}

	query := `SELECT COUNT(*) FROM vouchers v LEFT JOIN routers r ON r.id = v.router_id`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	var n int64
	if err := s.db.sql.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, wrapDBError("count vouchers", err)
	}
	return n, nil
}

// ListBatches returns the distinct batch names, newest first.
func (s *VoucherStore) ListBatches(ctx context.Context) ([]string, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT DISTINCT batch FROM vouchers WHERE batch <> '' ORDER BY batch DESC`)
	if err != nil {
		return nil, wrapDBError("list voucher batches", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var batch string
		if err := rows.Scan(&batch); err != nil {
			return nil, wrapDBError("list voucher batches", err)
		}
		out = append(out, batch)
	}
	return out, wrapDBError("list voucher batches", rows.Err())
}

// VouchersByIDs loads the given vouchers, skipping unknown ids.
func (s *VoucherStore) VouchersByIDs(ctx context.Context, ids []int64) ([]Voucher, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT`+voucherColumns+voucherFrom+` WHERE v.id IN (`+placeholders+`) ORDER BY v.id`, args...)
	if err != nil {
		return nil, wrapDBError("load vouchers by id", err)
	}
	defer rows.Close()

	var out []Voucher
	for rows.Next() {
		voucher, err := scanVoucher(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, voucher)
	}
	return out, wrapDBError("load vouchers by id", rows.Err())
}

// Redeem consumes one redemption of a voucher inside a transaction, so two
// simultaneous portal logins cannot both consume a single-use key.
//
// The updated row is re-read afterwards so callers can show the remaining
// allowance and the validity window.
func (s *VoucherStore) Redeem(ctx context.Context, id int64, at time.Time) (Voucher, error) {
	at = at.UTC().Truncate(time.Second)

	tx, err := s.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Voucher{}, wrapDBError("begin voucher redeem", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a harmless no-op

	row := tx.QueryRowContext(ctx, `SELECT`+voucherColumns+voucherFrom+` WHERE v.id = ?`, id)
	voucher, err := scanVoucher(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Voucher{}, ErrNotFound
	}
	if err != nil {
		return Voucher{}, err
	}
	if err := voucher.Redeemable(at); err != nil {
		return Voucher{}, err
	}

	uses := voucher.Uses + 1
	activated := voucher.ActivatedAt
	if activated == nil {
		first := at
		activated = &first
	}
	expires := voucher.ExpiresAt
	if expires == nil && voucher.ActivatedAt == nil && voucher.DurationMinutes > 0 {
		end := activated.Add(voucher.Duration())
		expires = &end
	}
	status := VoucherActive
	if voucher.MaxUses > 0 && uses >= voucher.MaxUses {
		status = VoucherUsed
	}

	if _, err := tx.ExecContext(ctx, `
        UPDATE vouchers SET
            status = ?, uses = ?,
            activated_at = COALESCE(activated_at, ?),
            expires_at = COALESCE(expires_at, ?),
            last_used_at = ?
        WHERE id = ?`,
		string(status), uses, stampValue(*activated), stampPtrValue(expires), stamp(at), id); err != nil {
		return Voucher{}, wrapDBError("redeem voucher", err)
	}
	if err := tx.Commit(); err != nil {
		return Voucher{}, wrapDBError("commit voucher redeem", err)
	}
	return s.Get(ctx, id)
}

// SetStatus forces a lifecycle state, used by the admin actions (disable,
// re-enable, mark as used).
func (s *VoucherStore) SetStatus(ctx context.Context, id int64, status VoucherStatus) error {
	if !status.Valid() {
		return fmt.Errorf("database: unknown voucher status %q", status)
	}
	res, err := s.db.sql.ExecContext(ctx, `UPDATE vouchers SET status = ? WHERE id = ?`, string(status), id)
	if err != nil {
		return wrapDBError("set voucher status", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkPushed records that the voucher's hotspot user was provisioned on the
// device, so the UI can show which keys work even while the controller is down.
func (s *VoucherStore) MarkPushed(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{stamp(at.UTC())}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.sql.ExecContext(ctx,
		`UPDATE vouchers SET pushed_at = ? WHERE id IN (`+placeholders+`)`, args...)
	return wrapDBError("mark vouchers pushed", err)
}

// Delete removes a single voucher.
func (s *VoucherStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.sql.ExecContext(ctx, `DELETE FROM vouchers WHERE id = ?`, id)
	if err != nil {
		return wrapDBError("delete voucher", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBatch removes every voucher of a batch. When onlyUnused is true the
// redeemed keys are kept for the audit trail.
func (s *VoucherStore) DeleteBatch(ctx context.Context, batch string, onlyUnused bool) (int64, error) {
	query := `DELETE FROM vouchers WHERE batch = ?`
	args := []any{batch}
	if onlyUnused {
		query += ` AND status IN (?, ?)`
		args = append(args, string(VoucherUnused), string(VoucherExpired))
	}
	res, err := s.db.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, wrapDBError("delete voucher batch", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("delete voucher batch", err)
	}
	return affected, nil
}

// SyncExpired flags vouchers whose validity window lapsed. It is cheap and runs
// before listings so the operator never sees a stale "active" badge.
func (s *VoucherStore) SyncExpired(ctx context.Context, at time.Time) (int64, error) {
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE vouchers SET status = ?
        WHERE status IN (?, ?) AND expires_at IS NOT NULL AND expires_at <= ?`,
		string(VoucherExpired), string(VoucherUnused), string(VoucherActive), stamp(at.UTC()))
	if err != nil {
		return 0, wrapDBError("expire vouchers", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDBError("expire vouchers", err)
	}
	return affected, nil
}

// VoucherStats aggregates the voucher ledger for the dashboard.
type VoucherStats struct {
	Total    int64
	Unused   int64
	Active   int64
	Used     int64
	Expired  int64
	Disabled int64
	// Pushed counts vouchers whose hotspot user already exists on a device.
	Pushed int64
	// BilledCents is the face value of every voucher that was redeemed at
	// least once (uses > 0), regardless of the current status.
	BilledCents int64
	// FaceValueCents is the face value of every voucher ever generated.
	FaceValueCents int64
}

// Stats aggregates voucher counts for all devices, or for one device when
// routerID > 0.
func (s *VoucherStore) Stats(ctx context.Context, routerID int64) (VoucherStats, error) {
	where := ""
	var args []any
	if routerID > 0 {
		where = ` WHERE router_id = ?`
		args = append(args, routerID)
	}

	// One aggregate row for the money and provisioning counters.
	var stats VoucherStats
	if err := s.db.sql.QueryRowContext(ctx, `
        SELECT COUNT(*),
               COALESCE(SUM(CASE WHEN pushed_at IS NOT NULL THEN 1 ELSE 0 END), 0),
               COALESCE(SUM(CASE WHEN uses > 0 THEN price_cents ELSE 0 END), 0),
               COALESCE(SUM(price_cents), 0)
        FROM vouchers`+where, args...).
		Scan(&stats.Total, &stats.Pushed, &stats.BilledCents, &stats.FaceValueCents); err != nil {
		return VoucherStats{}, wrapDBError("voucher totals", err)
	}

	rows, err := s.db.sql.QueryContext(ctx, `SELECT status, COUNT(*) FROM vouchers`+where+` GROUP BY status`, args...)
	if err != nil {
		return VoucherStats{}, wrapDBError("voucher stats", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			status string
			count  int64
		)
		if err := rows.Scan(&status, &count); err != nil {
			return VoucherStats{}, wrapDBError("voucher stats", err)
		}
		switch VoucherStatus(status) {
		case VoucherUnused:
			stats.Unused = count
		case VoucherActive:
			stats.Active = count
		case VoucherUsed:
			stats.Used = count
		case VoucherExpired:
			stats.Expired = count
		case VoucherDisabled:
			stats.Disabled = count
		}
	}
	return stats, wrapDBError("voucher stats", rows.Err())
}

// CountsByRouter returns the number of vouchers bound to each router id.
func (s *VoucherStore) CountsByRouter(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.db.sql.QueryContext(ctx,
		`SELECT router_id, COUNT(*) FROM vouchers WHERE router_id IS NOT NULL GROUP BY router_id`)
	if err != nil {
		return nil, wrapDBError("count vouchers by router", err)
	}
	defer rows.Close()

	out := map[int64]int64{}
	for rows.Next() {
		var (
			routerID int64
			count    int64
		)
		if err := rows.Scan(&routerID, &count); err != nil {
			return nil, wrapDBError("count vouchers by router", err)
		}
		out[routerID] = count
	}
	return out, wrapDBError("count vouchers by router", rows.Err())
}

func scanVoucher(row scanner) (Voucher, error) {
	var (
		v           Voucher
		routerID    sql.NullInt64
		status      string
		createdAt   string
		pushedAt    sql.NullString
		activatedAt sql.NullString
		expiresAt   sql.NullString
		lastUsedAt  sql.NullString
	)
	err := row.Scan(&v.ID, &v.Code, &v.Batch, &routerID, &v.RouterName, &v.Profile,
		&v.DurationMinutes, &v.DataLimitMB, &v.DeviceLimit, &v.PriceCents, &status,
		&v.Uses, &v.MaxUses, &v.Note, &createdAt, &pushedAt, &activatedAt,
		&expiresAt, &lastUsedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Voucher{}, err
		}
		return Voucher{}, wrapDBError("scan voucher", err)
	}

	if routerID.Valid {
		id := routerID.Int64
		v.RouterID = &id
	}
	v.Status = VoucherStatus(status)
	v.CreatedAt = parseStamp(sql.NullString{String: createdAt, Valid: createdAt != ""})
	v.PushedAt = parseStampPtr(pushedAt)
	v.ActivatedAt = parseStampPtr(activatedAt)
	v.ExpiresAt = parseStampPtr(expiresAt)
	v.LastUsedAt = parseStampPtr(lastUsedAt)
	return v, nil
}

func normalizedLimit(limit int) int {
	if limit <= 0 {
		return 200
	}
	if limit > 2000 {
		return 2000
	}
	return limit
}

func maxInt(v, min int) int {
	if v < min {
		return min
	}
	return v
}

func maxInt64(v, min int64) int64 {
	if v < min {
		return min
	}
	return v
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// nullableInt64 converts an optional id into a driver friendly value.
func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
