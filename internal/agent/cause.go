package agent

import (
	"context"
	"errors"
	"strconv"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	basecause "github.com/toyinogun/AIITsupport/internal/cause"
)

// cause turns a failure into a fixed token for logs and spans, read from the
// error's type, never its text (AC-17). It adds the agent's own tokens
// ("not_open", "anthropic_<status>", "mcp_rpc_<code>") to the shared ones
// from internal/cause, checked in the same order as before the split.
func cause(err error) string {
	var (
		api *anthropic.Error
		rpc *jsonrpc.Error
	)
	switch {
	case err == nil, errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return basecause.Of(err)
	case errors.Is(err, ErrNotOpen):
		return "not_open"
	case errors.As(err, &api):
		return "anthropic_" + strconv.Itoa(api.StatusCode)
	case errors.As(err, &rpc):
		return "mcp_rpc_" + strconv.FormatInt(rpc.Code, 10)
	}
	return basecause.Of(err)
}
