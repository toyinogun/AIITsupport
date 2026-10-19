// Package prompt holds the agent's system prompt, embedded from system.md
// (spec 0008). Changing it is a code change; the eval (#20) runs on it.
package prompt

import _ "embed"

// System is the first system block, marked for prompt caching.
//
//go:embed system.md
var System string
