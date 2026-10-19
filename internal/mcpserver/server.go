// Package mcpserver serves the tool layer over MCP (spec 0007): stateless
// streamable HTTP at /mcp behind a body cap and a bearer token, with a
// receiving middleware that refuses any tools/call whose _meta identity is
// missing or malformed before the dispatcher sees it.
package mcpserver

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/propagation"

	"github.com/toyinogun/AIITsupport/internal/tools"
)

// Path is where the MCP endpoint is mounted.
const Path = "/mcp"

const serverName = "noord-toolserver"

// Options configures the endpoint.
type Options struct {
	Token           string // TOOLSERVER_TOKEN
	MaxRequestBytes int64  // TOOLSERVER_MAX_REQUEST_BYTES
	Logger          *slog.Logger
}

// Handler returns the /mcp handler for d.
func Handler(d *tools.Dispatcher, opts Options) (http.Handler, error) {
	if opts.Token == "" || opts.MaxRequestBytes <= 0 || opts.Logger == nil {
		return nil, errors.New("mcpserver: needs a token, a positive body cap, and a logger")
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: "1"}, nil)
	for _, t := range d.Registry().Tools() {
		srv.AddTool(&mcp.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema()}, toolHandler(d, t.Name))
	}
	srv.AddReceivingMiddleware(requireMeta(d.Registry(), opts.Logger))

	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return requireToken(opts.Token, capBody(opts.MaxRequestBytes, extractTrace(sdk))), nil
}

// extractTrace reads the W3C trace context the agent's client sends, so the
// dispatcher's tool.call span joins the agent's trace (spec 0008).
func extractTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// toolHandler hands the call to the dispatcher with the requester the
// middleware parsed. A dispatcher answer is always a tool result, never a
// protocol error, so the model reads every refusal.
func toolHandler(d *tools.Dispatcher, name string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r, ok := requesterFrom(ctx)
		if !ok { // the middleware always sets it; refuse rather than guess
			return nil, errInvalidMeta
		}
		resp := d.Call(ctx, tools.Request{Tool: name, Args: req.Params.Arguments, Requester: r})
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: resp.Text}},
			StructuredContent: resp.Data,
			IsError:           resp.IsError,
		}, nil
	}
}

// errInvalidMeta is the JSON RPC invalid params error for bad identity (AC-2).
var errInvalidMeta = &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "missing or malformed _meta identity"}

// requireMeta refuses a tools/call without well formed _meta identity. It
// logs the tool name only, and only when it is a real tool: the name comes
// from the model.
func requireMeta(reg *tools.Registry, logger *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call, ok := req.(*mcp.CallToolRequest)
			if !ok || call.Params == nil {
				return nil, errInvalidMeta
			}
			r, err := parseMeta(call.Params.Meta)
			if err != nil {
				name := "unknown"
				if _, known := reg.Lookup(call.Params.Name); known {
					name = call.Params.Name
				}
				logger.WarnContext(ctx, "tool_call_rejected", "tool", name)
				return nil, errInvalidMeta
			}
			return next(withRequester(ctx, r), method, req)
		}
	}
}

type requesterKey struct{}

func withRequester(ctx context.Context, r tools.Requester) context.Context {
	return context.WithValue(ctx, requesterKey{}, r)
}

func requesterFrom(ctx context.Context) (tools.Requester, bool) {
	r, ok := ctx.Value(requesterKey{}).(tools.Requester)
	return r, ok
}

// requireToken compares SHA-256 digests in constant time, so neither the
// token nor its length leaks through timing. A bad token runs nothing.
func requireToken(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(presented))
		if !ok || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+serverName+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// capBody cuts a request body off at max bytes.
func capBody(maxBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBytes {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		next.ServeHTTP(w, r)
	})
}
