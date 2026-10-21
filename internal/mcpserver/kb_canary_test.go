package mcpserver_test

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/toyinogun/AIITsupport/internal/canary"
	"github.com/toyinogun/AIITsupport/internal/logging"
	"github.com/toyinogun/AIITsupport/internal/mcpclient"
	"github.com/toyinogun/AIITsupport/internal/mcpserver"
	"github.com/toyinogun/AIITsupport/internal/telemetry"
	"github.com/toyinogun/AIITsupport/internal/tools"
	"github.com/toyinogun/AIITsupport/internal/zammad/zammadtest"
)

// covers: spec 0014 AC-15. kb_search over MCP and a ticket_update with
// kb_review whose tag fails, logged through logging.New and exported
// through NewRedactingExporter: no log line or span holds the query, an
// article's title or body, or the note, and kb_review_tag_failed holds only
// its cause.
func TestCanaryKBSearch(t *testing.T) {
	const (
		query = "zebra"
		title = "CANARY-KB-TITLE zebra share"
		body  = "CANARY-KB-BODY open the zebra share"
		note  = "CANARY-KB-NOTE answered from the zebra article"
	)
	hunt, err := canary.Seed(filepath.Join("..", "..", "seed", "directory", "noord.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	r := requester()
	hunt.AnyCase(r.SlackID)
	for _, s := range []string{title, body, note, query} {
		hunt.Text(s)
	}

	exported := tracetest.NewInMemoryExporter()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSyncer(telemetry.NewRedactingExporter(exported))))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	logs := &bytes.Buffer{}
	m := startMFA(t, logging.New(logs, "debug", "toolserver"), "U0ITLEAD01", "auth-1", "+31 6 0000 0000")
	published := time.Date(2026, 1, 1, 7, 0, 0, 0, time.UTC)
	m.zammad.AddKBCategory(3, "VPN and remote work")
	m.zammad.AddKBAnswer(zammadtest.KBAnswer{CategoryID: 3, Title: title, Body: "<p>" + body + "</p>", Updated: published, Published: &published})
	m.zammad.Intercept = func(req *http.Request) (zammadtest.Fault, bool) {
		return zammadtest.Fault{Status: http.StatusInternalServerError, Message: "tags down " + note}, req.URL.Path == "/api/v1/tags/add"
	}

	c, err := mcpclient.New(m.srv.URL+mcpserver.Path, token)
	if err != nil {
		t.Fatal(err)
	}
	found, err := c.Call(t.Context(), r, tools.ToolKBSearch, map[string]any{"query": query})
	if err != nil || found.IsError || found.Data["count"] != 1.0 {
		t.Fatalf("kb_search = %+v, %v", found, err)
	}
	created, err := c.Call(t.Context(), r, tools.ToolTicketCreate, map[string]any{"title": "Zebra share", "body": "how do I reach it"})
	if err != nil || created.IsError {
		t.Fatalf("ticket_create = %+v, %v", created, err)
	}
	r.TicketID = int64(created.Data["ticket_id"].(float64))
	updated, err := c.Call(t.Context(), r, tools.ToolTicketUpdate, map[string]any{"state": "closed", "note": note, "kb_review": true})
	if err != nil || updated.IsError || updated.Data["kb_review_tagged"] != false {
		t.Fatalf("ticket_update = %+v, %v", updated, err)
	}

	out := logs.String()
	for _, want := range []string{`"tool":"kb_search"`, `"msg":"kb_review_tag_failed"`, `"cause":"zammad_500"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the flow did not run: no %s in\n%s", want, out)
		}
	}
	values := append(canary.LogValues(logs.Bytes()), canary.SpanValues(exported.GetSpans())...)
	if leaks := hunt.Leaks(values); len(leaks) > 0 {
		t.Errorf("logs or spans hold KB text, the note, or personal data:\n%s\nlogs:\n%s", strings.Join(leaks, "\n"), out)
	}
}
