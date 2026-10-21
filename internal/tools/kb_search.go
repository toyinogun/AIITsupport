package tools

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/toyinogun/AIITsupport/internal/audit"
	"github.com/toyinogun/AIITsupport/internal/zammad"
)

// kbLocale is the one knowledge base locale the assistant answers from.
const kbLocale = "en-us"

// kbZone is where an article's updated date and today are read (spec 0014,
// AC-4): Noord's own calendar, never UTC.
const kbZone = "Europe/Amsterdam"

// LoadKBZone loads Europe/Amsterdam. cmd/toolserver embeds the zone data
// (time/tzdata, the image has none) and refuses to start when this fails.
func LoadKBZone() (*time.Location, error) {
	zone, err := time.LoadLocation(kbZone)
	if err != nil {
		return nil, fmt.Errorf("load the knowledge base time zone %s: %w", kbZone, err)
	}
	return zone, nil
}

// Bounds of kb_search's query, part of the schema the model sees.
const (
	minKBQueryRunes = 2
	maxKBQueryRunes = 100
)

// kbReadConcurrency is how many answers one search reads at a time.
const kbReadConcurrency = 4

// kbSearchLimitFactor is the search limit sent to Zammad, as a multiple of
// KB_SEARCH_CANDIDATES: room for hits in other locales and duplicates.
const kbSearchLimitFactor = 2

// kbSearchTool is kb_search (spec 0014, AC-2): a read tool over Zammad's own
// knowledge base search, registered always because it needs only Zammad.
func kbSearchTool() Tool {
	return Tool{
		Name: ToolKBSearch,
		Description: "Search Noord's IT knowledge base (the help articles) for the requester's how to question. " +
			"Returns at most a few published articles, best match first, each with its id, title, category, updated date, " +
			"a stale flag set by code, and its text. Cite an article only with the marker [[kb:<id>]]; never write a link.",
		Tier: audit.TierRead,
		Fields: []Field{
			{
				Name: "query", Required: true, MinRunes: minKBQueryRunes, MaxRunes: maxKBQueryRunes, NoNewline: true, Trim: true, FreeText: true,
				Description: "A short English search query of one to three key words, such as vpn or password. Every word must " +
					"appear in an article, so leave out filler words. Translate Dutch and fix typos first.",
			},
		},
		Target:      TargetNone,
		StoreResult: []string{"count", "answer_ids", "stale_ids"},
		Handler:     ReadHandler(kbSearch),
	}
}

// kbArticle is one result the model reads.
type kbArticle struct {
	id       int
	title    string
	category string
	updated  time.Time // a date in Europe/Amsterdam
	stale    bool
	body     string
	url      string
}

func (a kbArticle) data() map[string]any {
	out := map[string]any{
		"id": a.id, "title": a.title, "category": a.category,
		"updated": a.updated.Format(time.DateOnly), "stale": a.stale, "body": a.body,
	}
	if a.url != "" {
		out["url"] = a.url
	}
	return out
}

// kbSearch runs the search, reads the first KB_SEARCH_CANDIDATES en-us
// answer hits, keeps the published ones, ranks them by the word score, and
// returns the top KB_SEARCH_LIMIT with stale computed (spec 0014, AC-2 to
// AC-4). Any Zammad error fails the call, never with partial results; only
// a hit deleted since indexing (404) is skipped (AC-12).
func kbSearch(ctx context.Context, d Deps, in Input) (Result, error) {
	meta, err := d.Helpdesk.kbMeta(ctx)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	hits, err := d.Helpdesk.api.SearchKB(ctx, meta.ID, kbLocale, in.Args["query"], kbSearchLimitFactor*d.KB.SearchCandidates)
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	reads, err := d.Helpdesk.readCandidates(ctx, meta.ID, kbCandidateHits(hits, d.KB.SearchCandidates))
	if err != nil {
		return Result{}, helpdeskFailure(err)
	}
	words := kbQueryWords(in.Args["query"])
	var candidates []kbCandidate
	for i, r := range reads {
		if !r.ok || !kbPublished(r.tr, in.Now) {
			continue
		}
		text := plainText(r.tr.Body)
		candidates = append(candidates, kbCandidate{read: r, text: text, score: kbScore(words, r.tr.Title, text), order: i})
	}
	today := kbToday(d.KB, in.Now)
	var articles []kbArticle
	for _, c := range kbRank(candidates, d.KB.SearchLimit) {
		articles = append(articles, d.Helpdesk.article(meta, c.read.tr, c.text, today, d.KB))
	}
	return kbResult(articles, d.KB.StaleAfterDays), nil
}

// kbCandidateHits are the first n distinct en-us answer hits in Zammad's
// order (newest first). They are counted before any read, so a candidate
// skipped later does not free a slot for another (spec 0014, AC-3).
func kbCandidateHits(hits []zammad.KBHit, n int) []zammad.KBHit {
	var out []zammad.KBHit
	seen := map[int]bool{}
	for _, hit := range hits {
		if len(out) == n {
			break
		}
		if hit.Locale != kbLocale || seen[hit.AnswerID] {
			continue
		}
		seen[hit.AnswerID] = true
		out = append(out, hit)
	}
	return out
}

// kbRead is one candidate's answer; ok is false when it was skipped (gone,
// or no such translation).
type kbRead struct {
	tr zammad.AnswerTranslation
	ok bool
}

// readCandidates reads every candidate, kbReadConcurrency at a time, all
// inside the call's one timeout. The first error fails the whole read: no
// partial results, even from a candidate that would rank last.
func (h *Helpdesk) readCandidates(ctx context.Context, kbID int, hits []zammad.KBHit) ([]kbRead, error) {
	reads := make([]kbRead, len(hits))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(kbReadConcurrency)
	for i, hit := range hits {
		g.Go(func() error {
			tr, ok, err := h.answerTranslation(gctx, kbID, hit)
			if err != nil {
				return fmt.Errorf("read knowledge base answer: %w", err)
			}
			reads[i] = kbRead{tr: tr, ok: ok}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return reads, nil
}

func kbResult(articles []kbArticle, staleAfterDays int) Result {
	results := make([]map[string]any, 0, len(articles))
	answerIDs, staleIDs := make([]int, 0, len(articles)), make([]int, 0, len(articles))
	for _, a := range articles {
		results = append(results, a.data())
		answerIDs = append(answerIDs, a.id)
		if a.stale {
			staleIDs = append(staleIDs, a.id)
		}
	}
	text := "No knowledge base articles found."
	if len(articles) > 0 {
		text = "Found " + strconv.Itoa(len(articles)) + " knowledge base articles."
	}
	return Result{
		Result: resultData(map[string]any{
			"count": len(articles), "stale_after_days": staleAfterDays, "results": results,
			"answer_ids": answerIDs, "stale_ids": staleIDs,
		}),
		Text:   text,
		Hidden: []string{"answer_ids", "stale_ids"},
	}
}

// kbPublished is Zammad's own published rule (spec 0014, AC-3):
// published_at set and not after now, and archived_at unset or after now.
// internal_at is never later than published_at, so it is not read. Drafts,
// internal, scheduled, and archived answers never reach the model.
func kbPublished(tr zammad.AnswerTranslation, now time.Time) bool {
	return tr.PublishedAt != nil && !tr.PublishedAt.After(now) && (tr.ArchivedAt == nil || tr.ArchivedAt.After(now))
}

// kbToday is the day the stale flag counts from: KB_STALE_AS_OF when set,
// else now, read in Europe/Amsterdam (spec 0014, AC-4).
func kbToday(s KBSettings, now time.Time) time.Time {
	if !s.StaleAsOf.IsZero() {
		return civilDate(s.StaleAsOf, time.UTC)
	}
	return civilDate(now, s.Zone)
}

// civilDate is t's calendar date in zone, as midnight UTC, so dates compare
// and count days without any zone's clock changes.
func civilDate(t time.Time, zone *time.Location) time.Time {
	y, m, day := t.In(zone).Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
}

// kbStale is true when updated is before today minus staleAfterDays days.
// It is a function of these three values only; the model never decides
// what is old.
func kbStale(updated, today time.Time, staleAfterDays int) bool {
	return updated.Before(today.AddDate(0, 0, -staleAfterDays))
}

// kbMeta returns the knowledge base id and category titles, loading them on
// first success like the ticket states: a failed load is not cached, and
// the lock is never held across the request.
func (h *Helpdesk) kbMeta(ctx context.Context) (zammad.KBMeta, error) {
	h.mu.Lock()
	meta := h.kb
	h.mu.Unlock()
	if meta != nil {
		return *meta, nil
	}
	loaded, err := h.api.KBInit(ctx)
	if err != nil {
		return zammad.KBMeta{}, fmt.Errorf("load knowledge base: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kb == nil {
		h.kb = &loaded
	}
	return *h.kb, nil
}

// answerTranslation reads a hit's answer with its translation's body; ok is
// false when the answer is gone (404) or has no such translation. The hit's
// translation id is asked for as the content id, as Zammad's own search
// links do; when the translation names another content, it is read again.
func (h *Helpdesk) answerTranslation(ctx context.Context, kbID int, hit zammad.KBHit) (zammad.AnswerTranslation, bool, error) {
	contentID := hit.TranslationID
	for range 2 {
		answer, err := h.api.GetAnswer(ctx, kbID, hit.AnswerID, contentID)
		if zammad.Status(err) == http.StatusNotFound {
			return zammad.AnswerTranslation{}, false, nil
		}
		if err != nil {
			return zammad.AnswerTranslation{}, false, err
		}
		tr, ok, err := answer.Translation(hit.TranslationID)
		if err != nil || !ok || tr.HasBody {
			return tr, ok, err
		}
		if tr.ContentID == contentID {
			break
		}
		contentID = tr.ContentID
	}
	return zammad.AnswerTranslation{}, false, fmt.Errorf("zammad: answer %d sent no body for its translation", hit.AnswerID)
}

// article builds one result: the body text cut at KB_BODY_MAX_RUNES, the
// category title from the cache, the updated date and stale flag, and the
// portal link when ZAMMAD_PUBLIC_URL is set and the category is known
// (spec 0014, AC-3).
func (h *Helpdesk) article(meta zammad.KBMeta, tr zammad.AnswerTranslation, text string, today time.Time, s KBSettings) kbArticle {
	updated := civilDate(tr.UpdatedAt, s.Zone)
	category, known := meta.Categories[tr.CategoryID][tr.KBLocaleID]
	a := kbArticle{
		id: tr.AnswerID, title: tr.Title, category: category, updated: updated,
		stale: kbStale(updated, today, s.StaleAfterDays), body: cutText(text, s.BodyMaxRunes),
	}
	if known && h.publicURL != "" {
		a.url = fmt.Sprintf("%s/help/%s/%d/%d", h.publicURL, kbLocale, tr.CategoryID, tr.AnswerID)
	}
	return a
}
