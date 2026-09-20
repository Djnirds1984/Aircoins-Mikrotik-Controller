package domain

import "time"

// Administrator roles.
const (
	RoleSuperAdmin = "superadmin"
	RoleOperator   = "operator"
	RoleViewer     = "viewer"
)

// Admin is a panel administrator.
type Admin struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	Disabled     bool
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// CanWrite reports whether this administrator may change configuration.
func (a *Admin) CanWrite() bool {
	return a.Role == RoleSuperAdmin || a.Role == RoleOperator
}

// IsSuperAdmin reports whether this administrator manages other admins.
func (a *Admin) IsSuperAdmin() bool { return a.Role == RoleSuperAdmin }

// RoleLabel renders the role for the interface.
func (a *Admin) RoleLabel() string {
	switch a.Role {
	case RoleSuperAdmin:
		return "Super administrator"
	case RoleOperator:
		return "Operator"
	case RoleViewer:
		return "Viewer"
	default:
		return a.Role
	}
}
