package approvalapi_test

import (
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

var spanRecorder = sync.OnceValue(func() *tracetest.SpanRecorder {
	rec := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	return rec
})

// approvalIDs returns the approval_id attribute of each ended span called name.
func approvalIDs(rec *tracetest.SpanRecorder, name string, from int) []string {
	var ids []string
	for _, s := range rec.Ended()[from:] {
		if s.Name() != name {
			continue
		}
		id := ""
		for _, a := range s.Attributes() {
			if a.Key == "approval_id" {
				id = a.Value.AsString()
			}
		}
		ids = append(ids, id)
	}
	return ids
}

// covers: spec 0016 AC-3. The decide span and the executed tool.call of an
// approved action carry the approval id, so the dashboard can find them.
func TestDecideSpansCarryApprovalID(t *testing.T) {
	rec := spanRecorder()
	e := newEnv(t)
	id := e.request()
	from := len(rec.Ended())
	if res, err := e.client.Decide(t.Context(), id, approver, "", true); err != nil || !res.Moved {
		t.Fatalf("decide = %+v, %v", res, err)
	}
	for _, name := range []string{"approval.decide", "tool.call"} {
		got := approvalIDs(rec, name, from)
		if len(got) != 1 || got[0] != id.String() {
			t.Errorf("%s approval_id = %v, want [%s]", name, got, id)
		}
	}
}

// covers: spec 0016 AC-3. The cancel span carries the approval id.
func TestCancelSpanCarriesApprovalID(t *testing.T) {
	rec := spanRecorder()
	e := newEnv(t)
	id := e.accessRequest()
	from := len(rec.Ended())
	if _, err := e.client.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if got := approvalIDs(rec, "approval.cancel", from); len(got) != 1 || got[0] != id.String() {
		t.Errorf("approval.cancel approval_id = %v, want [%s]", got, id)
	}
}
