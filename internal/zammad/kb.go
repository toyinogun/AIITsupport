package zammad

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NewCategory is a knowledge base category in one locale.
type NewCategory struct {
	Title      string
	Icon       string // a Font Awesome code such as f023
	KBLocaleID int
}

// NewAnswer is a knowledge base answer in one locale. Body is HTML.
type NewAnswer struct {
	CategoryID int
	Title      string
	Body       string
	KBLocaleID int
}

// Answer is a knowledge base answer as the API returns it. Assets holds
// Zammad's asset map (the answer, its translations, and their contents).
type Answer struct {
	ID     int                        `json:"id"`
	Assets map[string]json.RawMessage `json:"assets"`
}

type translationBody struct {
	Title      string       `json:"title"`
	KBLocaleID int          `json:"kb_locale_id"`
	Content    *contentBody `json:"content_attributes,omitempty"`
}

type contentBody struct {
	Body string `json:"body"`
}

// CreateCategory adds a top level category and returns its id.
func (c *Client) CreateCategory(ctx context.Context, kbID int, cat NewCategory) (int, error) {
	body := map[string]any{
		"category_icon": cat.Icon,
		"parent_id":     nil,
		"translations_attributes": []translationBody{
			{Title: cat.Title, KBLocaleID: cat.KBLocaleID},
		},
	}
	var out struct {
		ID int `json:"id"`
	}
	err := c.Do(ctx, Request{Method: http.MethodPost, Path: fmt.Sprintf("/api/v1/knowledge_bases/%d/categories", kbID), Body: body, Out: &out})
	return out.ID, err
}

// CreateAnswer adds a draft answer and returns its id.
func (c *Client) CreateAnswer(ctx context.Context, kbID int, a NewAnswer) (int, error) {
	body := map[string]any{
		"category_id": a.CategoryID,
		"translations_attributes": []translationBody{
			{Title: a.Title, KBLocaleID: a.KBLocaleID, Content: &contentBody{Body: a.Body}},
		},
	}
	var out Answer
	err := c.Do(ctx, Request{Method: http.MethodPost, Path: fmt.Sprintf("/api/v1/knowledge_bases/%d/answers", kbID), Body: body, Out: &out})
	return out.ID, err
}

// PublishAnswer publishes an answer, so Noord users see it in the portal.
func (c *Client) PublishAnswer(ctx context.Context, kbID, answerID int) error {
	return c.Do(ctx, Request{Method: http.MethodPost, Path: fmt.Sprintf("/api/v1/knowledge_bases/%d/answers/%d/publish", kbID, answerID), Body: map[string]any{}})
}

// GetAnswer reads one answer with its translations, and the contents
// (bodies) named by contentIDs; Zammad sends no content it was not asked for.
func (c *Client) GetAnswer(ctx context.Context, kbID, answerID int, contentIDs ...int) (Answer, error) {
	path := fmt.Sprintf("/api/v1/knowledge_bases/%d/answers/%d", kbID, answerID)
	if len(contentIDs) > 0 {
		ids := make([]string, len(contentIDs))
		for i, id := range contentIDs {
			ids[i] = strconv.Itoa(id)
		}
		path += "?include_contents=" + strings.Join(ids, ",")
	}
	var out Answer
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: path, Out: &out})
	return out, err
}

// KBMeta is what the knowledge base init read gives the assistant token
// (spec 0014, AC-3): the knowledge base id and each category's title per KB
// locale. The init read names no locale code; the search hit's portal path
// carries it instead.
type KBMeta struct {
	ID int
	// Categories maps a category id to its title in each KB locale id.
	Categories map[int]map[int]string
}

// KBInit reads the knowledge base's id and category titles
// (POST /api/v1/knowledge_bases/init). Zammad has at most one knowledge
// base; an inactive one comes back empty, which is an error here.
func (c *Client) KBInit(ctx context.Context) (KBMeta, error) {
	var out struct {
		KnowledgeBase map[string]struct {
			ID     int  `json:"id"`
			Active bool `json:"active"`
		} `json:"KnowledgeBase"`
		CategoryTranslations map[string]struct {
			Title      string `json:"title"`
			KBLocaleID int    `json:"kb_locale_id"`
			CategoryID int    `json:"category_id"`
		} `json:"KnowledgeBaseCategoryTranslation"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/knowledge_bases/init", Body: map[string]any{}, Out: &out}); err != nil {
		return KBMeta{}, err
	}
	meta := KBMeta{Categories: map[int]map[int]string{}}
	for _, kb := range out.KnowledgeBase {
		if !kb.Active {
			continue
		}
		if meta.ID != 0 {
			return KBMeta{}, errors.New("zammad: more than one active knowledge base")
		}
		meta.ID = kb.ID
	}
	if meta.ID == 0 {
		return KBMeta{}, errors.New("zammad: no active knowledge base")
	}
	for _, t := range out.CategoryTranslations {
		if meta.Categories[t.CategoryID] == nil {
			meta.Categories[t.CategoryID] = map[int]string{}
		}
		meta.Categories[t.CategoryID][t.KBLocaleID] = t.Title
	}
	return meta, nil
}

// KBHit is one answer translation the knowledge base search found.
type KBHit struct {
	TranslationID int
	AnswerID      int
	// Locale is the system locale code in the hit's portal path, such as en-us.
	Locale string
}

// answerTranslationType is the search hit kind of an answer translation.
const answerTranslationType = "KnowledgeBase::Answer::Translation"

// portalAnswerPath is an answer's public portal path as the search reports
// it: /help/<locale>/<category id>[-slug]/<answer id>[-slug] (probed in
// spec 0014, AC-17).
var portalAnswerPath = regexp.MustCompile(`^/help/([a-z]{2,3}(?:-[a-z0-9]{2,8})*)/\d+(?:-[^/]*)?/(\d+)(?:-[^/]*)?$`)

// SearchKB runs Zammad's knowledge base search (POST
// /api/v1/knowledge_bases/search, undocumented; spec 0014 AC-17) in the
// public flavor, so Zammad itself returns only published answers. It keeps
// the answer translation hits in Zammad's order, which this endpoint pins to
// newest first; every other hit kind is dropped. limit is sent explicitly:
// with none, Zammad returns every match. An answer hit whose portal path
// does not parse is an error, not a silent drop: Zammad changed shape.
func (c *Client) SearchKB(ctx context.Context, kbID int, locale, query string, limit int) ([]KBHit, error) {
	body := map[string]any{
		"knowledge_base_id": kbID,
		"locale":            locale,
		"query":             query,
		"limit":             limit,
		"flavor":            "public",
		"index":             answerTranslationType,
		"url_type":          "public",
		"highlight_enabled": false,
	}
	var out struct {
		Details []struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"details"`
	}
	if err := c.Do(ctx, Request{Method: http.MethodPost, Path: "/api/v1/knowledge_bases/search", Body: body, Out: &out}); err != nil {
		return nil, err
	}
	hits := make([]KBHit, 0, len(out.Details))
	for _, d := range out.Details {
		if d.Type != answerTranslationType {
			continue
		}
		m := portalAnswerPath.FindStringSubmatch(d.URL)
		if m == nil {
			return nil, errors.New("zammad: a knowledge base search hit has an unexpected portal path")
		}
		answerID, err := strconv.Atoi(m[2])
		if err != nil || answerID < 1 || d.ID < 1 {
			return nil, errors.New("zammad: a knowledge base search hit has an unexpected id")
		}
		hits = append(hits, KBHit{TranslationID: d.ID, AnswerID: answerID, Locale: m[1]})
	}
	return hits, nil
}

// AnswerTranslation is one answer with one of its translations, read from
// GetAnswer's assets. internal_at is not read: Zammad's own published rule
// ignores it (spec 0014, AC-3).
type AnswerTranslation struct {
	AnswerID    int
	CategoryID  int
	PublishedAt *time.Time
	ArchivedAt  *time.Time

	TranslationID int
	KBLocaleID    int
	ContentID     int
	Title         string
	UpdatedAt     time.Time // the translation's, which the seed backdates
	// Body is the content's HTML. HasBody is false when GetAnswer was not
	// asked for this translation's content.
	Body    string
	HasBody bool
}

// Translation finds one translation of the answer in its assets; ok is
// false when the answer has no translation with that id. An error means
// the assets are not Zammad's shape.
func (a Answer) Translation(translationID int) (AnswerTranslation, bool, error) {
	var answers map[string]struct {
		ID          int        `json:"id"`
		CategoryID  int        `json:"category_id"`
		PublishedAt *time.Time `json:"published_at"`
		ArchivedAt  *time.Time `json:"archived_at"`
	}
	var translations map[string]struct {
		ID         int       `json:"id"`
		Title      string    `json:"title"`
		KBLocaleID int       `json:"kb_locale_id"`
		AnswerID   int       `json:"answer_id"`
		ContentID  int       `json:"content_id"`
		UpdatedAt  time.Time `json:"updated_at"`
	}
	var contents map[string]struct {
		Body string `json:"body"`
	}
	for key, dst := range map[string]any{
		"KnowledgeBaseAnswer":                   &answers,
		"KnowledgeBaseAnswerTranslation":        &translations,
		"KnowledgeBaseAnswerTranslationContent": &contents,
	} {
		raw, ok := a.Assets[key]
		if !ok {
			continue
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			return AnswerTranslation{}, false, fmt.Errorf("zammad: decode answer %d assets: %w", a.ID, err)
		}
	}
	answer, ok := answers[strconv.Itoa(a.ID)]
	if !ok {
		return AnswerTranslation{}, false, fmt.Errorf("zammad: answer %d is missing from its own assets", a.ID)
	}
	tr, ok := translations[strconv.Itoa(translationID)]
	if !ok || tr.AnswerID != a.ID {
		return AnswerTranslation{}, false, nil
	}
	out := AnswerTranslation{
		AnswerID: a.ID, CategoryID: answer.CategoryID,
		PublishedAt: answer.PublishedAt, ArchivedAt: answer.ArchivedAt,
		TranslationID: tr.ID, KBLocaleID: tr.KBLocaleID, ContentID: tr.ContentID, Title: tr.Title, UpdatedAt: tr.UpdatedAt,
	}
	if content, ok := contents[strconv.Itoa(tr.ContentID)]; ok {
		out.Body, out.HasBody = content.Body, true
	}
	return out, true, nil
}
