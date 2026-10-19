package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// Zammad state names the tools map to and recognise.
const (
	zammadOpen            = "open"
	zammadPendingReminder = "pending reminder"
	zammadClosed          = "closed"
	zammadMerged          = "merged"
)

// Helpdesk is the Zammad client plus what the tools add: the default group
// and the ticket states, loaded once per process.
type Helpdesk struct {
	api       HelpdeskAPI
	group     string // ZAMMAD_DEFAULT_GROUP
	publicURL string // ZAMMAD_PUBLIC_URL, no trailing slash; empty for no ticket links

	mu     sync.Mutex
	byName map[string]int
	byID   map[int]string
}

// NewHelpdesk wraps api; tickets go to group, and ticket links start with
// publicURL (empty for none).
func NewHelpdesk(api HelpdeskAPI, group, publicURL string) *Helpdesk {
	return &Helpdesk{api: api, group: group, publicURL: strings.TrimSuffix(publicURL, "/")}
}

// states returns the state maps, loading them on first success. A failed
// load is not cached, so the next call tries again. The lock is never held
// across the request, so each caller is bounded by its own context; callers
// that race on a cold cache each load, and the first to finish is kept.
func (h *Helpdesk) states(ctx context.Context) (byName map[string]int, byID map[int]string, err error) {
	h.mu.Lock()
	byName, byID = h.byName, h.byID
	h.mu.Unlock()
	if byName != nil {
		return byName, byID, nil
	}
	list, err := h.api.TicketStates(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load ticket states: %w", err)
	}
	byName, byID = make(map[string]int, len(list)), make(map[int]string, len(list))
	for _, s := range list {
		byName[s.Name] = s.ID
		byID[s.ID] = s.Name
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byName == nil {
		h.byName, h.byID = byName, byID
	}
	return h.byName, h.byID, nil
}

// customer returns the requester's one active Zammad user, whose login
// equals the directory account exactly; ok is false when there is none (or,
// failing closed, more than one).
func (h *Helpdesk) customer(ctx context.Context, account string) (zammad.User, bool, error) {
	users, err := h.api.UsersByLogin(ctx, account)
	if err != nil {
		return zammad.User{}, false, err
	}
	if len(users) != 1 {
		return zammad.User{}, false, nil
	}
	return users[0], true, nil
}
