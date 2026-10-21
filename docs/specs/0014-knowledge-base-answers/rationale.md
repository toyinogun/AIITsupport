# 0014. Knowledge base answers: rationale

The decision record for [index.md](index.md). `/develop` builds from `index.md`; this file is the why.

## Context

Noord's IT person answers the same how to questions every week: how to get on the VPN from home, what to do when the label printer jams, how to open a shared mailbox. The answers mostly exist in the Zammad knowledge base (spec 0006), but nobody reads it, and the parts people do read are wrong. That is deliberate in this engagement: the seeded KB has 15 articles, of which two pairs contradict each other (a lockout clears after 30 or 15 minutes; the 2019 and 2025 password rules), two describe retired systems (SecureLink VPN, SMS sign in codes), two send people to colleagues who have left, and two cover the same password reset in slightly different words. The scope's done when for #16 asks that answers cite their article, and that a contradiction is either escalated or settled by a stated rule.

The forces are specific. The truth about each article (`status`, `contradicts`, `retired_system`, `dead_contact`) lives only in the seed files for the eval; Zammad holds just the title, category, body, and backdated dates. So whatever the assistant knows about "outdated" has to come from dates and text, not labels. The project rule that the model never writes a URL (spec 0011, the reset link) means a "cite the article" requirement cannot be met by the model typing a link. The token budget is tight: cache reads count, about five model calls fit in a conversation (journal 0058), and each existing flow already spends three or four. The tool server is the only process that holds the Zammad token, and every tool call leaves an audit pair (spec 0007). The KB is editable by Zammad's KB editors, so article text is not trusted input.

The KB also overlaps the action flows: the lockout article tells people to wait 15 minutes, while the assistant can unlock them now; the shared drive article sends people to a leaver, while the access flow (spec 0013) asks the group's real owner. Answering from the KB when a tool can act would make the assistant worse, not better.

Leaving this undecided means the "Anything else" path keeps opening tickets for questions the KB already answers, which is exactly the queue the engagement is meant to shrink. Slice 6 is second in the cut order, so the design has to be small enough to build in a day or two, or be cut cleanly.

## Options considered

### Option 1: A read tool over Zammad's KB search, a stated date rule with a code computed stale flag, code built citation links (chosen)

The tool server gains `kb_search`, which calls Zammad's own KB search (Elasticsearch, already proven to find SecureLink in spec 0006's verify), reads up to three published hits in full, and returns them as plain text with their `updated` date, a `stale` flag computed by code against a two year line, and the portal URL. The prompt states the rules: action flows first; newest wins and the user hears about the older article; stale only answers stay open for IT; never pass on a named contact; no match leaves the ticket open. The model cites with a marker, and agent code turns markers into links only for ids this conversation's searches returned. `ticket_update` gains a `kb_review` flag that tags the ticket for IT.

**Pros**:
- Uses the client system as it is; search quality and scale come with Zammad, not with code we own.
- The two things a model is worst at here (inventing links, deciding what is "old") are code, and testable.
- No schema change and no new credential; the reads fit the existing `knowledge_base.reader` role.
- The `kb-review` tag turns the KB's mess into a work list, which is the honest FDE outcome.

**Cons**:
- Zammad's KB search endpoint is undocumented; it needs a probe and a fallback.
- Whether two articles disagree is still model judgment.
- One search plus up to three answer reads per call.

### Option 2: The tool server ranks every answer itself

`kb_search` reads every published answer from Zammad on each call and ranks them in Go by word matches in title and body, newest first on ties.

**Pros**:
- Deterministic and fully testable against the fake Zammad; no dependency on an undocumented endpoint or on Elasticsearch.
- Easy to add synonyms or Dutch stems later in code.

**Cons**:
- Search becomes code we own and tune; it will be worse than Elasticsearch at fuzzy matching and typos.
- Reads the whole KB on every call; fine for 15 articles, wrong for a real client KB of hundreds.
- It quietly works around the client's system instead of using it, which reads poorly in a handover.

### Option 3: The whole KB in the system prompt

Render all 15 articles (about 2,000 tokens) into the prompt with their dates, and let the model answer and cite from that.

**Pros**:
- No tool, no extra model call, no Zammad dependency at answer time.
- The model sees every contradiction at once.

**Cons**:
- Cache reads count toward the 30000 token budget (journal 0058): about 2,000 more tokens on every model call of every conversation, including lockouts that never touch the KB. That breaks the access happy path, which already fits exactly.
- The KB would be frozen into the binary or loaded at start, so an article Ruben fixes is not seen until a restart.
- Does not scale past a toy KB; a pattern nobody should copy.

### Option 4: Never answer, always route

The assistant searches the KB but only attaches the best article to the ticket for IT, and tells the user IT will follow up.

**Pros**:
- No risk of the assistant passing on an outdated answer.
- Smallest prompt change.

**Cons**:
- Does not meet the scope's done when (answers that cite their article).
- Every KB question still lands in Ruben's queue, the exact pain the engagement exists to fix.

## Rationale

Option 1 is the only one that answers users and stays honest about a KB the engagement made untrustworthy on purpose. The forces that decide it are the budget, the no URL rule, and the absence of truth labels in Zammad. Option 3 fails on the budget alone: journal 0058 showed the access path fits the 30000 tokens exactly, and 2,000 tokens more on every call would end it with `token_budget`. Option 4 fails the done when. Between 1 and 2, the deciding force is that this is an FDE engagement: the deliverable runs on the client's system as it is, and Zammad already has a working search over this KB. The undocumented endpoint is a real risk, so the spec makes Option 2's ranking the named fallback behind a probe gate, the same pattern spec 0006 used for its unproven Zammad facts.

Since Zammad holds no truth labels, the honesty rules have to be built from what it does hold: dates and text. Code owns what dates can decide (the stale flag, a fixed and testable line), and the model owns what only reading can decide (whether two bodies disagree), under one stated rule, newest wins. Code checked citation markers follow from the existing rule that the model never writes a URL. They also turn "answers cite the article" into something the eval can check: a cited id must be one the search returned. The `kb_review` tag on a closed ticket, rather than an open ticket or a channel post, keeps "the user is helped" and "the KB needs work" in separate lists for Ruben, without adding urgent noise to `#it-escalations`.

Action flows win because the KB is the weaker source whenever a tool can act: the lockout article says wait 15 minutes when the assistant can unlock now, and the shared drive article names a leaver when the access flow asks the real owner. The named contact rule covers what the stale flag cannot see (`shared-drive-access` was updated in 2025 but names someone who left in June 2026), and it keeps working as more people leave.

### Calls made while writing (the recommended items)

- **Up to 3 full bodies, not titles plus a read tool**: three bodies are about 600 tokens and put a contradicting pair side by side in one call; a separate read tool would cost a model call the budget cannot spare. Runner up: 5 results, better odds of catching both sides of a conflict, at about 1,000 tokens per call.
- **Drop article hrefs in the HTML to text step**: the model needs no URLs from article bodies, and a planted link in an article then cannot reach a reply. Runner up: keep hrefs as text, which would make the no URL rule depend on the prompt alone.
- **Published and not archived only, filtered in code**: the assistant's role may be able to read drafts; whether it can is a probe question, and the filter makes the answer irrelevant. Runner up: trust Zammad's search to return published answers only, which is unverified.
- **Cache the KB id, locale, and categories in `Helpdesk`**: the same pattern as the cached ticket states; these do not change between resets. Runner up: read them on every call, one more Zammad call per search.
- **`url` stays visible to the model**: it is not a secret and the history needs it to resolve markers on a later turn; the prompt rule plus the marker check cover misuse. Runner up: strip it like `reset_url` and rebuild it in agent code, which would move `ZAMMAD_PUBLIC_URL` into the agent's settings for no safety gain.
- **No per conversation search limit**: the token budget already bounds it, and a read tool has no write limit. Runner up: a `KB_SEARCH_PER_TURN` setting, which adds a knob nobody needs yet.
- **`kb_review` requires a note**: a tag without a reason gives Ruben nothing to fix. Runner up: allow a bare tag.
- **`agent_kb_marker_dropped` as a log line with a count only**: a forged or stale citation is a signal worth seeing in the logs, and a count carries no content. Runner up: no log, relying on the eval.
- **Stale line as days (`KB_STALE_AFTER_DAYS`), not a duration**: Go's duration parser has no day unit, and `TICKET_RETENTION_DAYS` already set the pattern.

### Changes from the cross check (2026-10-21)

An independent read only pass on another model found nine decision gaps and six soundness points; the engineer chose to apply every recommended fix. The spec now pins: where and how markers are resolved (once in `run()`, from stored results paired as `listCalls` does), and a code filter that removes any link code did not build, in every reply; a boolean schema field kind for `kb_review`, with the tag only after a successful update; the search hit mapping and the published filter by timestamps (Zammad can schedule publishing and archiving); `time/tzdata` in the tool server and a fixed `KB_STALE_AS_OF` for the eval, since the stale flag would otherwise drift (`reset-your-password` crosses two years on 2026-11-18); the portal URL rules; an explicit access routing rule and the refused `ticket_create` path; when `kb_review` is true; the fallback ranking in full; a measurable budget gate with a margin, plus reruns of the access and MFA flows; and a code built fallback post so a KB answer is never lost after the close. The suggestion to drop the ticket on the happy path was not taken: the engineer chose a ticket for every answer, and its cost is now recorded in Consequences.

### Changes after the build (2026-10-21)

The build's probe and live runs (`verify.md`, journals 0059 and 0060) showed three places where the spec's assumptions did not hold. The engineer chose the recommended fix for each.

- **The search order is newest first, not relevance.** Zammad's REST search pins `updated_at` descending and needs every query word to match. With a limit of 3, an older article that contradicts a newer one could fall out (live: `lock` returned 2, 15, and 3, and dropped the 2020 lockout article), which defeats the newest wins rule it was meant to feed. Chosen: Zammad still decides which articles match; code reads up to `KB_SEARCH_CANDIDATES` of them and ranks with the word score the spec already defined for the fallback, so a title match outranks a newer body only match. Checked against the live hits, `lock` then gives 2, 1, and 15, and `vpn` gives 14, 13, and 8. Runners up: raise `KB_SEARCH_LIMIT` to 5 (more tokens on every call, and a broad query still drops the oldest), or accept newest first and let the eval count the misses. This takes part of Option 2 (code ranks) without its main con (reading the whole KB), since Zammad's search still filters first.
- **The token budget was missed on every KB path** (28330 to 30489 against 27000). The cause is the prefix, not the articles: the cached prompt and tools are about 6660 tokens, every one of the four calls reads them again, and the budget counted those reads in full (the question journal 0058 left open). Chosen: weigh a cache read by its price relative to an uncached input token, from `eval/pricing.yaml` (a tenth today), so the budget tracks what a conversation costs, with no new setting and no hardcoded ratio. A KB answer then counts about 12000, mostly the one time cache write. Runners up: raise the default to 40000 (no change to what is counted, but every new prompt section eats into it again), or a cost cap in micro USD (exact, but a new setting, a renamed failure, and shifted eval numbers). Shrinking the results could not close the gap: the bodies are about 1000 tokens of the 30000. The new AC-14 target, 15000, is half the budget, so a follow up question still fits.
- **The published rule hid answers Zammad counts as published.** The spec also required `internal_at` unset or later; Zammad's own published scope reads only `published_at` and `archived_at`, and its validations keep `internal_at` at or before `published_at`. Chosen: match Zammad's rule. Runner up: keep the stricter rule, which hides an article that went internal before it was published.
- **Probe facts recorded in the ACs** (not decisions): the KB init read names no locale code (and this Zammad has no locales route), so `en-us` is checked on each hit's portal path; `GetAnswer` returns a body only for `?include_contents=<content id>`; the portal link needs no slugs. AC-17 now records the probe as done and keeps the fallback for a future Zammad upgrade.

### Changes from the cross check of the update (2026-10-21)

An independent read only pass on another model confirmed the ranking and budget numbers and found nine gaps; the engineer chose to apply every recommended fix. The spec now pins: the weight on scaled integer prices, rounded down once on the summed cache read term, with a full count when the input price is 0 or the ratio is above 1; `ModelPrice.BudgetTokens` in `eval` and the store returning the four sums, with `SumConversationTokens` kept but unused by the check; an explicit search `limit`; candidates counted before any read; word start matching on the full text before the body cut, Unicode words, timestamp then list position for ties, and the no scorable word case; reads at most 4 at a time inside one `TOOL_CALL_TIMEOUT`; AC-14 naming its three scenarios and their call counts, with raw and weighted counts and cold and warm cache recorded apart; and "approximately" for how the budget tracks cost.

### AC-13's live run is planted by hand (2026-10-21)

AC-13 first said the live test adds the planted article to the dev Zammad and removes it after. The build found that it cannot: the live test lives in `internal/agent`, and depguard's `agent-credential-boundary` denies `internal/zammad` to every file there, tests included. Planting needs Zammad admin rights, which only `cmd/seed` holds. The engineer chose to count a hand run instead: plant the article with the admin API, run the live question, delete the article, and record each step in `verify.md`. Runner up: a seed subcommand that plants and removes a test article, which would make the run repeatable but adds a write path to the seeder for one check; not worth it until the eval (#19, #20) needs the planted case on every run.

An independent read only pass on another model found the amendment sound and found that a hand run must be repeatable to count. It recommended five fixes, which the engineer applied: name the admin login (the `SEED_ZAMMAD_ADMIN_*` settings), record the article and the question as a plant recipe in `verify.md`, put the pass line in AC-13, and record the answer id, the ticket id, the times, and a 404 after the delete. The first build's run stays as a one time record.
