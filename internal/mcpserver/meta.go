package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/mail"
	"regexp"
	"unicode"

	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

// Exact formats for each _meta value (AC-2). Values are never trimmed.
var (
	slackUserID = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)
	channelID   = regexp.MustCompile(`^[CDG][A-Z0-9]{2,20}$`)
	threadTS    = regexp.MustCompile(`^\d{10}\.\d{6}$`)
)

const (
	maxEmailLen = 254
	maxTicketID = 1<<53 - 1
)

var errBadMeta = errors.New("missing or malformed _meta value")

// parseMeta reads identity from _meta only. Other keys are ignored.
func parseMeta(m map[string]any) (tools.Requester, error) {
	var r tools.Requester
	var ok bool
	if r.SlackID, ok = exact(m, mcpclient.MetaSlackID, slackUserID); !ok {
		return r, errBadMeta
	}
	if r.ChannelID, ok = exact(m, mcpclient.MetaChannelID, channelID); !ok {
		return r, errBadMeta
	}
	if r.ThreadTS, ok = exact(m, mcpclient.MetaThreadTS, threadTS); !ok {
		return r, errBadMeta
	}
	if r.Email, ok = email(m[mcpclient.MetaEmail]); !ok {
		return r, errBadMeta
	}
	if raw, present := m[mcpclient.MetaTicketID]; present {
		if r.TicketID, ok = ticketID(raw); !ok {
			return r, errBadMeta
		}
	}
	// The initiator is optional; when present it must be exactly code
	// (spec 0009, AC-2).
	if raw, present := m[mcpclient.MetaInitiator]; present {
		if s, _ := raw.(string); s != tools.InitiatorCode {
			return r, errBadMeta
		}
		r.Initiator = tools.InitiatorCode
	}
	return r, nil
}

func exact(m map[string]any, key string, re *regexp.Regexp) (string, bool) {
	s, ok := m[key].(string)
	return s, ok && re.MatchString(s)
}

// email accepts a bare address that net/mail parses back to itself.
func email(v any) (string, bool) {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > maxEmailLen {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return "", false
	}
	return s, true
}

// ticketID accepts a JSON integer from 1 to 2^53 - 1, decoded as a
// json.Number so no float rounding hides a bad value.
func ticketID(v any) (int64, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return 0, false
	}
	n, ok := decoded.(json.Number)
	if !ok {
		return 0, false
	}
	id, err := n.Int64()
	if err != nil || id < 1 || id > maxTicketID {
		return 0, false
	}
	return id, true
}
