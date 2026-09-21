package database

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// voucherAlphabet omits 0/O/1/I/L so printed voucher cards cannot be misread.
const voucherAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// voucherCodeExpr is the SQL normalisation used for code lookups: uppercase,
// without dashes or spaces. It lets a customer type "air4f7k92qx" and still be
// matched against the stored "AIR-4F7K-92QX".
const voucherCodeExpr = "replace(replace(upper(code), '-', ''), ' ', '')"

// VoucherCodeOptions controls the shape of generated voucher keys.
type VoucherCodeOptions struct {
	// Prefix is prepended to the random groups, e.g. "AIR".
	Prefix string
	// Groups is how many dash separated random groups to generate.
	Groups int
	// GroupLength is the number of characters per group.
	GroupLength int
}

func (o VoucherCodeOptions) withDefaults() VoucherCodeOptions {
	if strings.TrimSpace(o.Prefix) == "" {
		o.Prefix = "AIR"
	}
	if o.Groups <= 0 {
		o.Groups = 2
	}
	if o.Groups > 6 {
		o.Groups = 6
	}
	if o.GroupLength <= 0 {
		o.GroupLength = 4
	}
	if o.GroupLength > 8 {
		o.GroupLength = 8
	}
	return o
}

// DefaultVoucherCodeOptions is the shape used when the operator does not pick
// one: AIR-XXXX-XXXX, roughly 6.9e11 combinations.
func DefaultVoucherCodeOptions() VoucherCodeOptions {
	return VoucherCodeOptions{Prefix: "AIR", Groups: 2, GroupLength: 4}
}

// GenerateVoucherCode returns a cryptographically random, human friendly code
// such as "AIR-4F7K-92QX".
func GenerateVoucherCode(opts VoucherCodeOptions) (string, error) {
	opts = opts.withDefaults()

	groups := make([]string, 0, opts.Groups)
	for i := 0; i < opts.Groups; i++ {
		group, err := randomVoucherGroup(opts.GroupLength)
		if err != nil {
			return "", err
		}
		groups = append(groups, group)
	}

	prefix := strings.ToUpper(strings.Trim(strings.TrimSpace(opts.Prefix), "- "))
	code := strings.Join(groups, "-")
	if prefix != "" {
		code = prefix + "-" + code
	}
	if len(code) > 48 {
		return "", fmt.Errorf("database: voucher code %q is longer than 48 characters", code)
	}
	return code, nil
}

func randomVoucherGroup(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("database: voucher code randomness: %w", err)
	}
	out := make([]byte, length)
	for i, b := range buf {
		out[i] = voucherAlphabet[int(b)%len(voucherAlphabet)]
	}
	return string(out), nil
}

// VoucherLookupKey normalises a user typed code for comparison.
func VoucherLookupKey(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	code = strings.ReplaceAll(code, " ", "")
	return code
}

// FormatVoucherCode renders a code the way it is stored: uppercase and
// dash separated. It keeps the groups the operator typed.
func FormatVoucherCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	parts := strings.FieldsFunc(code, func(r rune) bool { return r == '-' || r == ' ' })
	return strings.Join(parts, "-")
}

// VoucherBatchLabel builds a default batch name such as "BATCH-20260921-1215".
func VoucherBatchLabel(at time.Time) string {
	return "BATCH-" + at.UTC().Format("20060102-1504")
}
