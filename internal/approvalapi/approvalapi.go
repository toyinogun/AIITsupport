// Package approvalapi serves the tool server's approval routes (spec 0012,
// AC-10 to AC-13) on the MCP listener, behind the same bearer token, body
// cap, and trace context extract as /mcp. They are plain JSON, never MCP
// tools: the model cannot list or call them. Only agent code does, with the
// Slack id of the person who clicked.
package approvalapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/db/queries"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/tools"
)

const tracerName = "github.com/toyinogun/AIITsupport/internal/approvalapi"

// maxList is how many approvals GET /approvals returns at most, and how
// many rows one POST /approvals/expire ends of each kind (AC-12, AC-13).
const maxList = 100

// clickerID is the Slack user id format _meta accepts (spec 0007, AC-2).
var clickerID = regexp.MustCompile(`^[UW][A-Z0-9]{2,20}$`)

// clickerEmail is the shape a decision's clicker_email must have (spec 0013,
// AC-13); the directory decides whether it names anyone.
var clickerEmail = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]{1,255}$`)

// Options configures the routes.
type Options struct {
	Token           string // TOOLSERVER_TOKEN
	MaxRequestBytes int64  // TOOLSERVER_MAX_REQUEST_BYTES
	Logger          *slog.Logger
}

// API is what the routes need from the tool layer.
type API struct {
	recorder *audit.Recorder
	d        *tools.Dispatcher
	logger   *slog.Logger
	tracer   trace.Tracer
}

// Handler returns the approval routes, to mount at mcpclient.ApprovalsPath
// and under it on the MCP listener's mux. The abandon grace is the
// dispatcher's APPROVAL_EXECUTE_GRACE.
func Handler(d *tools.Dispatcher, rec *audit.Recorder, opts Options) (http.Handler, error) {
	if d == nil || rec == nil || opts.Token == "" || opts.MaxRequestBytes <= 0 || d.ExecuteGrace() <= 0 || opts.Logger == nil {
		return nil, errors.New("approvalapi: needs the dispatcher, the recorder, a token, a positive body cap and grace, and a logger")
	}
	a := &API{recorder: rec, d: d, logger: opts.Logger, tracer: otel.Tracer(tracerName)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+mcpclient.ExpirePath, a.expire)
	mux.HandleFunc("POST "+mcpclient.ApprovalsPath+"/{id}/decision", a.decide)
	mux.HandleFunc("POST "+mcpclient.ApprovalsPath+"/{id}/cancel", a.cancel)
	mux.HandleFunc("GET "+mcpclient.ApprovalsPath+"/{id}", a.get)
	mux.HandleFunc("GET "+mcpclient.ApprovalsPath, a.list)
	return mcpserver.RequireToken(opts.Token, mcpserver.CapBody(opts.MaxRequestBytes, mcpserver.ExtractTrace(mux))), nil
}

// decide applies one click (spec 0012 AC-10, spec 0013 AC-13). It reads the
// approval first: a group owner approval's decider is the AD account the
// clicker's email maps to, which must be the listed owner. On a moved
// approve, the execution runs detached from the request under its own
// timeout, so a client that goes away never stops a write halfway; the
// answer follows it.
func (a *API) decide(w http.ResponseWriter, r *http.Request) {
	ctx, span := a.tracer.Start(r.Context(), "approval.decide")
	defer span.End()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, ok := decodeDecision(r)
	if !ok {
		http.Error(w, "the body must be exactly {clicker, approve} plus an optional clicker_email", http.StatusBadRequest)
		return
	}
	// Detached: a committed approve must always be followed by its execution,
	// even when the client has gone away. The recorder bounds the write.
	dctx := context.WithoutCancel(ctx)
	by, err := a.decider(dctx, id, body)
	switch {
	case errors.Is(err, audit.ErrApprovalNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		a.unavailable(ctx, w)
		return
	}
	dec, err := a.recorder.Decide(dctx, id, by, body.Approve)
	switch {
	case errors.Is(err, audit.ErrApprovalNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		a.unavailable(ctx, w)
		return
	}
	outcome := "refused"
	if dec.Moved {
		outcome = dec.Approval.Status
	}
	a.logger.InfoContext(ctx, "approval_decided", "approval_id", id.String(), "outcome", outcome)
	span.SetAttributes(attribute.String("outcome", outcome))

	if dec.Moved && body.Approve {
		a.d.Execute(dctx, dec.Approval)
	}
	view, err := a.view(dctx, id)
	if err != nil {
		// The move is committed: answer with what Decide saw, not a 503 the
		// agent would read as "nothing happened". The tick settles the rest.
		a.logger.ErrorContext(ctx, "audit_unavailable")
		view = a.baseView(dec.Approval)
	}
	writeJSON(w, mcpclient.DecisionResponse{Moved: dec.Moved, Reason: reasonToken(dec.Reason), Approval: view})
}

// expire ends overdue pending approvals, then abandons approved ones whose
// execution never confirmed within the grace (AC-12). It never runs a stored
// call. A failure after the expiry committed still answers 503: the tick
// tries again, and both moves are guarded.
func (a *API) expire(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	expired, err := a.recorder.Expire(ctx, maxList)
	if err != nil {
		a.unavailable(ctx, w)
		return
	}
	abandoned, err := a.recorder.Abandon(ctx, a.d.ExecuteGrace(), maxList)
	if err != nil {
		a.unavailable(ctx, w)
		return
	}
	for kind, rows := range map[string]int{"expired": len(expired), "abandoned": len(abandoned)} {
		if rows > 0 {
			a.logger.InfoContext(ctx, "approvals_expired", "kind", kind, "rows", rows)
		}
	}
	writeJSON(w, mcpclient.ExpireResponse{Expired: len(expired), Abandoned: len(abandoned)})
}

// decider is who the click came from. For a group owner approval it maps
// clicker_email to an account (spec 0013, AC-13); no email, no match, or an
// account the approval does not list leaves the account empty, so Decide
// refuses it as not an allowed approver and records no decider account. For
// kind it the email is ignored.
func (a *API) decider(ctx context.Context, id uuid.UUID, body mcpclient.DecisionRequest) (audit.Decider, error) {
	by := audit.Decider{SlackID: body.Clicker}
	ap, err := a.recorder.Approval(ctx, id)
	if err != nil || ap.ApproverKind != string(audit.ApproverGroupOwner) || body.ClickerEmail == "" {
		return by, err
	}
	account, ok, err := a.d.ResolveDecider(ctx, body.ClickerEmail)
	if err != nil {
		return by, err
	}
	if ok && slices.Contains(ap.AllowedApprovers, account) {
		by.Account = account
	}
	return by, nil
}

// decodeDecision accepts exactly {clicker, approve} plus an optional
// clicker_email, with a Slack user id and an email of the right shape.
func decodeDecision(r *http.Request) (mcpclient.DecisionRequest, bool) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil || dec.Decode(&struct{}{}) != io.EOF || len(raw) < 2 || len(raw) > 3 {
		return mcpclient.DecisionRequest{}, false
	}
	var out mcpclient.DecisionRequest
	clicker, okC := raw["clicker"]
	approve, okA := raw["approve"]
	if !okC || !okA || json.Unmarshal(clicker, &out.Clicker) != nil || json.Unmarshal(approve, &out.Approve) != nil ||
		!bytes.Equal(bytes.TrimSpace(approve), []byte("true")) && !bytes.Equal(bytes.TrimSpace(approve), []byte("false")) {
		return mcpclient.DecisionRequest{}, false
	}
	if len(raw) == 3 {
		email, ok := raw["clicker_email"]
		if !ok || json.Unmarshal(email, &out.ClickerEmail) != nil || !clickerEmail.MatchString(out.ClickerEmail) {
			return mcpclient.DecisionRequest{}, false
		}
	}
	return out, clickerID.MatchString(out.Clicker)
}

// cancel ends a pending group owner approval whose owner cannot be reached
// in Slack (spec 0013, AC-11). The body must be exactly {"reason":
// "owner_unreachable"}; any other status or kind is 409 and writes nothing.
func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	ctx, span := a.tracer.Start(r.Context(), "approval.cancel")
	defer span.End()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !decodeCancel(r) {
		http.Error(w, `the body must be exactly {"reason": "owner_unreachable"}`, http.StatusBadRequest)
		return
	}
	dctx := context.WithoutCancel(ctx)
	_, moved, err := a.recorder.Cancel(dctx, id)
	switch {
	case errors.Is(err, audit.ErrApprovalNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		a.unavailable(ctx, w)
		return
	case !moved:
		http.Error(w, "not a pending group owner approval", http.StatusConflict)
		return
	}
	a.logger.InfoContext(ctx, "approval_cancelled", "approval_id", id.String())
	view, err := a.view(dctx, id)
	if err != nil {
		a.unavailable(ctx, w)
		return
	}
	writeJSON(w, view)
}

func decodeCancel(r *http.Request) bool {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&raw); err != nil || dec.Decode(&struct{}{}) != io.EOF || len(raw) != 1 {
		return false
	}
	var reason string
	return json.Unmarshal(raw["reason"], &reason) == nil && reason == mcpclient.CancelOwnerUnreachable
}

// reasonToken maps spec 0003's stored refusal reasons to the route's tokens.
func reasonToken(reason string) string {
	return map[string]string{
		"not an allowed approver":  mcpclient.ReasonNotAllowed,
		"requester cannot approve": mcpclient.ReasonSelf,
		"expired":                  mcpclient.ReasonExpired,
		"already decided":          mcpclient.ReasonDecided,
	}[reason]
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	view, err := a.view(r.Context(), id)
	switch {
	case errors.Is(err, audit.ErrApprovalNotFound):
		http.NotFound(w, r)
	case err != nil:
		a.unavailable(r.Context(), w)
	default:
		writeJSON(w, view)
	}
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	since, err := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
	if err != nil {
		http.Error(w, "since must be an RFC 3339 time", http.StatusBadRequest)
		return
	}
	list, err := a.recorder.ApprovalsSince(r.Context(), since, maxList)
	if err != nil {
		a.unavailable(r.Context(), w)
		return
	}
	// The list views carry no execution (one read per row); the tick only
	// needs ids and threads, and GET /approvals/{id} has the rest.
	views := make([]mcpclient.ApprovalView, 0, len(list))
	for _, ap := range list {
		views = append(views, a.baseView(ap))
	}
	writeJSON(w, views)
}

func (a *API) view(ctx context.Context, id uuid.UUID) (mcpclient.ApprovalView, error) {
	ap, err := a.recorder.Approval(ctx, id)
	if err != nil {
		return mcpclient.ApprovalView{}, err
	}
	return a.viewOf(ctx, ap)
}

// viewOf builds the single approval view (spec 0012 AC-13, spec 0013
// AC-10): executed and failed approvals carry how their execution ended, an
// expired one whether it was a cancel, and a group owner approval its owner.
func (a *API) viewOf(ctx context.Context, ap queries.Approval) (mcpclient.ApprovalView, error) {
	v := a.baseView(ap)
	if ap.ApproverKind == string(audit.ApproverGroupOwner) {
		a.ownerFields(ctx, ap, &v)
	}
	if ap.Status == mcpclient.StatusExpired {
		cancelled, err := a.recorder.Cancelled(ctx, ap.ID)
		if err != nil {
			return v, err
		}
		if cancelled {
			v.CancelReason = mcpclient.CancelOwnerUnreachable
		}
	}
	if ap.Status != mcpclient.StatusExecuted && ap.Status != mcpclient.StatusFailed {
		return v, nil
	}
	ex, ok, err := a.recorder.ExecutionOf(ctx, ap)
	if err != nil || !ok {
		return v, err
	}
	v.Execution = &mcpclient.ApprovalExecution{Outcome: string(ex.Outcome), Reason: ex.Reason}
	if len(ex.Result) > 0 {
		_ = json.Unmarshal(ex.Result, &v.Execution.Result) // our own stored JSON; a cut one shows no counts
		delete(v.Execution.Result, "_omitted")
	}
	return v, nil
}

// ownerFields adds a group owner approval's owner, read live, and the
// requester's reason while the approval holds it. A failed directory read
// leaves the owner fields empty, and the agent tries again on its next tick.
func (a *API) ownerFields(ctx context.Context, ap queries.Approval, v *mcpclient.ApprovalView) {
	if len(ap.AllowedApprovers) == 1 {
		v.ApproverAccount = ap.AllowedApprovers[0]
	}
	var args struct {
		Reason string `json:"reason"`
	}
	if len(ap.Arguments) > 0 {
		_ = json.Unmarshal(ap.Arguments, &args) // our own stored JSON; a broken one shows no reason
		v.Reason = args.Reason
	}
	owner, err := a.d.GroupOwnerView(ctx, ap)
	if err != nil {
		a.logger.WarnContext(ctx, "approval_owner_read_failed", "approval_id", ap.ID.String(), "cause", tools.Cause(err))
		return
	}
	v.ApproverName, v.ApproverEmail, v.NestedInto = owner.Name, owner.Email, owner.NestedInto
}

// baseView is the view without the execution.
func (a *API) baseView(ap queries.Approval) mcpclient.ApprovalView {
	v := mcpclient.ApprovalView{
		ID: ap.ID, ToolName: ap.ToolName, Status: ap.Status, DecidedBy: ap.DecidedBy.String,
		ExpiresAt: ap.ExpiresAt.UTC(), CreatedAt: ap.CreatedAt.UTC(),
		RequesterSlackID: ap.RequesterSlackID, RequesterAccount: ap.RequesterAccount.String,
		SlackChannelID: ap.SlackChannelID, SlackThreadTS: ap.SlackThreadTs,
		TicketID: ap.ZammadTicketID.Int64, TicketURL: a.d.TicketURL(ap.ZammadTicketID.Int64),
		ApproverKind: ap.ApproverKind, Target: ap.Target.String,
	}
	if ap.DecidedAt.Valid {
		at := ap.DecidedAt.Time.UTC()
		v.DecidedAt = &at
	}
	return v
}

func (a *API) unavailable(ctx context.Context, w http.ResponseWriter) {
	a.logger.ErrorContext(ctx, "audit_unavailable")
	http.Error(w, "audit log unavailable", http.StatusServiceUnavailable)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
