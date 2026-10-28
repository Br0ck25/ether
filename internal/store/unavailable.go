package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUnavailable reports whether err means the database server could not be
// reached or dropped the connection — a condition that clears on its own
// (network outage, server restart or failover) — rather than a problem
// with the statement or the data. Deadline errors count: a write bounded
// by a short timeout that expires is treated as an outage.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return unavailableCode(pe.Code)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) {
		return true
	}
	if pgconn.Timeout(err) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return true
	}
	// pgx reports a connection that died mid-statement this way.
	msg := err.Error()
	return strings.Contains(msg, "conn closed") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") || strings.Contains(msg, "unexpected EOF")
}

// unavailableCode classifies PostgreSQL SQLSTATEs that mean "the server is
// not there right now".
func unavailableCode(code string) bool {
	switch {
	case strings.HasPrefix(code, "08"): // connection exception
		return true
	case code == "57P01", code == "57P02", code == "57P03": // admin/crash shutdown, cannot connect now
		return true
	case code == "53300": // too many connections
		return true
	case code == "40001", code == "40P01": // serialization failure, deadlock: retry later
		return false
	}
	return false
}

// IsPermanent reports whether a PostgreSQL connection error will never
// succeed on retry: wrong password or user, a database or schema that does
// not exist, or missing privileges. A node starting against such a URL
// should stop and tell the owner instead of retrying forever.
func IsPermanent(err error) bool {
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return false
	}
	switch {
	case strings.HasPrefix(pe.Code, "28"): // invalid authorization (bad password, no such role)
		return true
	case pe.Code == "3D000", pe.Code == "3F000": // database / schema does not exist
		return true
	case pe.Code == "42501": // insufficient privilege
		return true
	}
	return false
}
