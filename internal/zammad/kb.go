package zammad

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// GetAnswer reads one answer with its contents.
func (c *Client) GetAnswer(ctx context.Context, kbID, answerID int) (Answer, error) {
	var out Answer
	err := c.Do(ctx, Request{Method: http.MethodGet, Path: fmt.Sprintf("/api/v1/knowledge_bases/%d/answers/%d?full=true", kbID, answerID), Out: &out})
	return out, err
}
