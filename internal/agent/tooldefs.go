package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"
)

// toolDefs caches the tool server's tools/list, converted once, for the life
// of the process. A failed fetch is not cached (spec 0008, The model loop).
// Concurrent first uses share one fetch, and each waiter still honours its
// own deadline.
type toolDefs struct {
	mu    sync.Mutex
	tools []anthropic.ToolParam
	fetch singleflight.Group
}

// get returns the cached definitions, fetching them on first use. user_lookup
// is left out: code owns that call.
func (d *toolDefs) get(ctx context.Context, src Tools) ([]anthropic.ToolParam, error) {
	d.mu.Lock()
	cached := d.tools
	d.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	ch := d.fetch.DoChan("tools", func() (any, error) {
		tools, err := fetchTools(ctx, src)
		if err != nil {
			return nil, err
		}
		d.mu.Lock()
		d.tools = tools
		d.mu.Unlock()
		return tools, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.([]anthropic.ToolParam), nil
	}
}

func fetchTools(ctx context.Context, src Tools) ([]anthropic.ToolParam, error) {
	listed, err := src.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	tools := make([]anthropic.ToolParam, 0, len(listed))
	for _, t := range listed {
		if t == nil || t.Name == toolUserLookup {
			continue
		}
		p, err := toToolParam(t)
		if err != nil {
			return nil, err
		}
		tools = append(tools, p)
	}
	return tools, nil
}

// toToolParam passes the name, description, and input schema through unchanged.
func toToolParam(t *mcp.Tool) (anthropic.ToolParam, error) {
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return anthropic.ToolParam{}, fmt.Errorf("tool %s: encode schema: %w", t.Name, err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return anthropic.ToolParam{}, fmt.Errorf("tool %s: the input schema is not an object", t.Name)
	}
	in := anthropic.ToolInputSchemaParam{Properties: schema["properties"]}
	if req, ok := schema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				in.Required = append(in.Required, s)
			}
		}
	}
	extra := map[string]any{}
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
		default:
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		in.ExtraFields = extra
	}
	p := anthropic.ToolParam{Name: t.Name, InputSchema: in}
	if t.Description != "" {
		p.Description = anthropic.String(t.Description)
	}
	return p, nil
}

// requestTools wraps the definitions for a request and marks the last one
// for prompt caching. The cached slice is never changed.
func requestTools(defs []anthropic.ToolParam) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(defs))
	for i := range defs {
		p := defs[i]
		if i == len(defs)-1 {
			p.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		out[i] = anthropic.ToolUnionParam{OfTool: &p}
	}
	return out
}
