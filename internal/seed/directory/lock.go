package directory

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-ldap/ldap/v3"
)

// ufLockout is the locked bit in msDS-User-Account-Control-Computed. It is
// the only reliable lock signal: lockoutTime stays set after a lock expires.
const ufLockout = 0x10

// ErrLockoutOff means the domain's lockout threshold is 0, so failed binds
// never lock anyone.
var ErrLockoutOff = errors.New("the domain lockout threshold is 0, so accounts never lock")

// LockState is a user's lock state as the Administrator sees it.
type LockState struct {
	Locked      bool
	BadPwdCount int
}

// LockoutThreshold reads how many failed binds lock an account.
func LockoutThreshold(conn ldap.Client, base string) (int, error) {
	entry, err := readOne(conn, base, "lockoutThreshold")
	if err != nil {
		return 0, err
	}
	if entry == nil {
		return 0, fmt.Errorf("domain root %s not found", base)
	}
	n, err := strconv.Atoi(entry.GetAttributeValue("lockoutThreshold"))
	if err != nil {
		return 0, fmt.Errorf("read lockoutThreshold: %w", err)
	}
	if n <= 0 {
		return 0, ErrLockoutOff
	}
	return n, nil
}

// Lock binds as the user with a wrong password threshold times, which is the
// only way to lock an AD account. It does not confirm the lock; State does.
func Lock(ctx context.Context, d Dialer, userDN, wrongPassword string, threshold int) error {
	conn, err := d.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	for range threshold {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("lock %s: %w", userDN, err)
		}
		err := conn.Bind(userDN, wrongPassword)
		switch {
		case err == nil:
			return fmt.Errorf("lock %s: the wrong password was accepted", userDN)
		case !ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials):
			return fmt.Errorf("lock %s: failed bind: %w", userDN, err)
		}
	}
	return nil
}

// LockAndConfirm locks a user and checks the computed lock flag as the
// Administrator, so a lock that did not take is an error, never a success.
func LockAndConfirm(ctx context.Context, admin ldap.Client, d Dialer, userDN, wrongPassword string, threshold int) error {
	if err := Lock(ctx, d, userDN, wrongPassword, threshold); err != nil {
		return err
	}
	state, err := State(admin, userDN)
	if err != nil {
		return err
	}
	if !state.Locked {
		return fmt.Errorf("%s is still unlocked after %d failed binds (badPwdCount %d)", userDN, threshold, state.BadPwdCount)
	}
	return nil
}

// State reads a user's lock state. admin must be bound as the Administrator,
// because the computed attribute is the only reliable lock signal (AC-8).
func State(admin ldap.Client, userDN string) (LockState, error) {
	entry, err := readOne(admin, userDN, "msDS-User-Account-Control-Computed", "badPwdCount")
	if err != nil {
		return LockState{}, err
	}
	if entry == nil {
		return LockState{}, fmt.Errorf("%s not found", userDN)
	}
	computed, err := strconv.Atoi(entry.GetAttributeValue("msDS-User-Account-Control-Computed"))
	if err != nil {
		return LockState{}, fmt.Errorf("read lock flag of %s: %w", userDN, err)
	}
	bad, _ := strconv.Atoi(entry.GetAttributeValue("badPwdCount")) // absent means 0
	return LockState{Locked: computed&ufLockout != 0, BadPwdCount: bad}, nil
}
