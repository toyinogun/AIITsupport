package ldap

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	goldap "github.com/go-ldap/ldap/v3"
)

// ufLockout is the locked bit in msDS-User-Account-Control-Computed. It is
// the only reliable lock signal: lockoutTime stays set after a lock expires.
const ufLockout = 0x10

// Attributes State reads. The computed one is constructed, so it is only
// returned when asked for by name.
const (
	attrComputedUAC = "msDS-User-Account-Control-Computed"
	attrLockoutTime = "lockoutTime"
	attrBadPwdCount = "badPwdCount"
)

// LockState is a user's lock state.
type LockState struct {
	Locked      bool
	BadPwdCount int
	// LockedAt is lockoutTime; zero when it is 0 or unset. It stays set after
	// a lock expires, so read Locked for the state.
	LockedAt time.Time
}

// State reads a user's lock state from the computed lock flag.
func State(conn goldap.Client, userDN string) (LockState, error) {
	entry, err := ReadOne(conn, userDN, attrComputedUAC, attrBadPwdCount, attrLockoutTime)
	if err != nil {
		return LockState{}, err
	}
	if entry == nil {
		return LockState{}, fmt.Errorf("%s not found", userDN)
	}
	computed, err := strconv.Atoi(entry.GetAttributeValue(attrComputedUAC))
	if err != nil {
		return LockState{}, fmt.Errorf("read lock flag of %s: %w", userDN, err)
	}
	bad, _ := strconv.Atoi(entry.GetAttributeValue(attrBadPwdCount)) // absent means 0
	lockedAt, err := fileTime(entry.GetAttributeValue(attrLockoutTime))
	if err != nil {
		return LockState{}, fmt.Errorf("read lockoutTime of %s: %w", userDN, err)
	}
	return LockState{Locked: computed&ufLockout != 0, BadPwdCount: bad, LockedAt: lockedAt}, nil
}

// LockoutDuration reads how long the domain keeps an account locked. forever
// is true when a lock lasts until an admin clears it.
func LockoutDuration(conn goldap.Client, base string) (d time.Duration, forever bool, err error) {
	entry, err := ReadOne(conn, base, "lockoutDuration")
	if err != nil {
		return 0, false, err
	}
	if entry == nil {
		return 0, false, fmt.Errorf("domain root %s not found", base)
	}
	raw := entry.GetAttributeValue("lockoutDuration")
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("read lockoutDuration: %w", err)
	}
	// AD stores it as a negative count of 100ns intervals. The smallest int64
	// (and 0, which no auto unlock can follow) means until an admin unlocks.
	if n == 0 || n == math.MinInt64 {
		return 0, true, nil
	}
	if n > 0 {
		return 0, false, errors.New("read lockoutDuration: not a negative interval")
	}
	return time.Duration(-n) * 100, false, nil
}

// fileTime converts a FILETIME (100ns intervals since 1601) to UTC. Empty and
// 0 give the zero time.
func fileTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("not a FILETIME: %w", err)
	}
	if n <= 0 {
		return time.Time{}, nil
	}
	const (
		perSecond = 10_000_000
		// secondsBeforeUnix is 1601-01-01 to 1970-01-01, where FILETIME and
		// Unix time count from.
		secondsBeforeUnix = 11_644_473_600
	)
	return time.Unix(n/perSecond-secondsBeforeUnix, (n%perSecond)*100).UTC(), nil
}
