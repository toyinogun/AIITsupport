-- Model usage and cost per call (spec 0003). Cost is in micro US dollars.

-- name: InsertLLMUsage :exec
INSERT INTO llm_usage (
    conversation_id, model, input_tokens, output_tokens,
    cache_read_tokens, cache_write_tokens, cost_microusd, pricing_date
) VALUES (
    @conversation_id, @model, @input_tokens, @output_tokens,
    @cache_read_tokens, @cache_write_tokens, @cost_microusd, @pricing_date
);

-- name: SumConversationTokens :one
-- SumConversationTokens is what AGENT_MAX_TOKENS_PER_TICKET is checked against.
SELECT COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_tokens), 0)::bigint AS tokens
FROM llm_usage
WHERE conversation_id = @conversation_id;

-- name: SumConversationUsage :one
SELECT
    COALESCE(SUM(input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(output_tokens), 0)::bigint AS output_tokens,
    COALESCE(SUM(cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(SUM(cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(SUM(cost_microusd), 0)::bigint AS cost_microusd
FROM llm_usage
WHERE conversation_id = @conversation_id;
