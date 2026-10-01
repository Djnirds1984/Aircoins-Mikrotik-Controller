package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrDeviceInvalid is returned when a device cannot be stored because its
// identity is incomplete: it wraps a human readable reason so the HTTP layer
// can surface it as a form error rather than parsing a message.
var ErrDeviceInvalid = errors.New("database: invalid device")

// Device is an operator-tracked client on a router.
//
// Only the durable identity is persisted: which router the device belongs to,
// its normalised MAC address, an optional friendly name and free-form notes.
// The volatile facts an operator actually reads on the DEVICES page - the
// current IP address, the DHCP hostname and how much paid session time is left
// - are fetched live from the router and are never written here, so a stored
// row can never show a stale address for a device that has since moved.
type Device struct {
	ID       int64
	RouterID int64
	// RouterName is resolved by a join for display; it is not a stored column.
	// It is empty when the router was deleted underneath the record.
	RouterName string
	// MAC is stored normalised (lowercase, no separators). Use FormatMAC to
	// render it and NormalizeMAC before comparing an incoming value.
	MAC   string
	Name  string
	Notes string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// MACAddress renders the stored MAC in the canonical colon-separated form.
func (d Device) MACAddress() string { return FormatMAC(d.MAC) }

// DeviceStore persists the operator's device inventory.
type DeviceStore struct{ db *DB }

// deviceColumns is the shared projection. The router name is a LEFT JOIN so a
// device whose router was removed still lists (with an empty name) instead of
// vanishing from the page.
const deviceColumns = `
    d.id, d.router_id, COALESCE(r.name, ''), d.mac, d.name, d.notes,
    d.created_at, d.updated_at`

// List returns every stored device, ordered by name then MAC, with the owning
// router's name resolved.
func (s *DeviceStore) List(ctx context.Context) ([]Device, error) {
	return s.query(ctx, `SELECT`+deviceColumns+`
        FROM devices d LEFT JOIN routers r ON r.id = d.router_id
        ORDER BY d.name COLLATE NOCASE, d.mac`)
}

// Get loads one device by id.
func (s *DeviceStore) Get(ctx context.Context, id int64) (Device, error) {
	row := s.db.sql.QueryRowContext(ctx, `SELECT`+deviceColumns+`
        FROM devices d LEFT JOIN routers r ON r.id = d.router_id WHERE d.id = ?`, id)
	device, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return device, err
}

// FindByMAC returns the stored device for a router and MAC address, or
// ErrNotFound when the device has not been saved. The MAC is normalised first
// so a value pasted in any RouterOS/URL/form spelling still matches.
func (s *DeviceStore) FindByMAC(ctx context.Context, routerID int64, mac string) (Device, error) {
	normalized := NormalizeMAC(mac)
	if normalized == "" {
		return Device{}, ErrNotFound
	}
	row := s.db.sql.QueryRowContext(ctx, `SELECT`+deviceColumns+`
        FROM devices d LEFT JOIN routers r ON r.id = d.router_id
        WHERE d.router_id = ? AND d.mac = ?`, routerID, normalized)
	device, err := s.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	return device, err
}

func (s *DeviceStore) query(ctx context.Context, sqlText string, args ...any) ([]Device, error) {
	rows, err := s.db.sql.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, wrapDBError("query devices", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		device, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, device)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDBError("query devices", err)
	}
	return out, nil
}

// Create stores a new device and returns it with its assigned id. The MAC is
// required and normalised; a second save of the same MAC on the same router is
// rejected as a duplicate rather than silently overwriting the first record.
func (s *DeviceStore) Create(ctx context.Context, device Device) (Device, error) {
	normalized := NormalizeMAC(device.MAC)
	if normalized == "" {
		return Device{}, fmt.Errorf("%w: a MAC address is required", ErrDeviceInvalid)
	}
	if device.RouterID <= 0 {
		return Device{}, fmt.Errorf("%w: a router is required", ErrDeviceInvalid)
	}
	ts := stamp(now())

	res, err := s.db.sql.ExecContext(ctx, `
        INSERT INTO devices (router_id, mac, name, notes, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?)`,
		device.RouterID, normalized,
		strings.TrimSpace(device.Name), strings.TrimSpace(device.Notes), ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return Device{}, fmt.Errorf("%s is already saved on this router", FormatMAC(normalized))
		}
		return Device{}, wrapDBError("create device", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Device{}, wrapDBError("create device id", err)
	}
	return s.Get(ctx, id)
}

// Update writes the mutable fields of an existing device. The identity
// (router + MAC) may be corrected too, so an operator who saved the wrong
// address can fix it without deleting and re-adding the record.
func (s *DeviceStore) Update(ctx context.Context, device Device) (Device, error) {
	normalized := NormalizeMAC(device.MAC)
	if normalized == "" {
		return Device{}, fmt.Errorf("%w: a MAC address is required", ErrDeviceInvalid)
	}
	if device.RouterID <= 0 {
		return Device{}, fmt.Errorf("%w: a router is required", ErrDeviceInvalid)
	}
	res, err := s.db.sql.ExecContext(ctx, `
        UPDATE devices SET router_id = ?, mac = ?, name = ?, notes = ?, updated_at = ?
        WHERE id = ?`,
		device.RouterID, normalized,
		strings.TrimSpace(device.Name), strings.TrimSpace(device.Notes),
		stamp(now()), device.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return Device{}, fmt.Errorf("%s is already saved on this router", FormatMAC(normalized))
		}
		return Device{}, wrapDBError("update device", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return Device{}, ErrNotFound
	}
	return s.Get(ctx, device.ID)
}

// Delete removes a device record. It touches only the controller's inventory:
// the client is left untouched on the router.
func (s *DeviceStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.sql.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return wrapDBError("delete device", err)
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of stored devices.
func (s *DeviceStore) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices`).Scan(&n)
	return n, wrapDBError("count devices", err)
}

func (s *DeviceStore) scan(row scanner) (Device, error) {
	var (
		d         Device
		createdAt string
		updatedAt string
	)
	err := row.Scan(&d.ID, &d.RouterID, &d.RouterName, &d.MAC, &d.Name, &d.Notes,
		&createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Device{}, err
		}
		return Device{}, wrapDBError("scan device", err)
	}
	d.CreatedAt = parseStamp(sql.NullString{String: createdAt, Valid: createdAt != ""})
	d.UpdatedAt = parseStamp(sql.NullString{String: updatedAt, Valid: updatedAt != ""})
	return d, nil
}
