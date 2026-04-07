package domain

import "time"

type DocumentFormat string

const (
	DocumentFormatMarkdown DocumentFormat = "markdown"
	DocumentFormatText     DocumentFormat = "text"
	DocumentFormatCode     DocumentFormat = "code"
	DocumentFormatHTML     DocumentFormat = "html"
	DocumentFormatJSON     DocumentFormat = "json"
)

type SuggestionStatus string

const (
	SuggestionStatusPending  SuggestionStatus = "pending"
	SuggestionStatusAccepted SuggestionStatus = "accepted"
	SuggestionStatusRejected SuggestionStatus = "rejected"
	SuggestionStatusStale    SuggestionStatus = "stale"
)

type ThreadStatus string

const (
	ThreadStatusOpen     ThreadStatus = "open"
	ThreadStatusResolved ThreadStatus = "resolved"
)

type Identity struct {
	Name string `json:"name"`
}

type Block struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	Start       int            `json:"start"`
	End         int            `json:"end"`
	Text        string         `json:"text"`
	Level       int            `json:"level,omitempty"`
	Format      DocumentFormat `json:"format,omitempty"`
	Language    string         `json:"language,omitempty"`
	LineStart   int            `json:"line_start,omitempty"`
	LineEnd     int            `json:"line_end,omitempty"`
	CreatedFrom string         `json:"created_from,omitempty"`
}

type Anchor struct {
	BlockID         string `json:"block_id"`
	Start           int    `json:"start"`
	End             int    `json:"end"`
	DocStart        int    `json:"doc_start"`
	DocEnd          int    `json:"doc_end"`
	Quote           string `json:"quote"`
	Prefix          string `json:"prefix,omitempty"`
	Suffix          string `json:"suffix,omitempty"`
	Revision        int64  `json:"revision"`
	Resolved        bool   `json:"resolved"`
	ResolvedBlockID string `json:"resolved_block_id,omitempty"`
}

type Document struct {
	ID         string         `json:"id"`
	Title      string         `json:"title"`
	Format     DocumentFormat `json:"format"`
	Source     string         `json:"source"`
	Revision   int64          `json:"revision"`
	Blocks     []Block        `json:"blocks"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	LastEdited string         `json:"last_edited,omitempty"`
}

type DocumentSummary struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Format    DocumentFormat `json:"format"`
	Revision  int64          `json:"revision"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type MatchSelector struct {
	Match      string `json:"match"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	Occurrence int    `json:"occurrence,omitempty"`
}

type DocumentRead struct {
	DocumentID string         `json:"document_id"`
	Revision   int64          `json:"revision"`
	Scope      string         `json:"scope"`
	Text       string         `json:"text"`
	Blocks     []Block        `json:"blocks,omitempty"`
	Selector   *MatchSelector `json:"selector,omitempty"`
}

type Comment struct {
	ID        string    `json:"id"`
	ThreadID  string    `json:"thread_id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type Thread struct {
	ID         string       `json:"id"`
	DocumentID string       `json:"document_id"`
	Anchor     Anchor       `json:"anchor"`
	Status     ThreadStatus `json:"status"`
	Author     string       `json:"author"`
	CreatedAt  time.Time    `json:"created_at"`
	UpdatedAt  time.Time    `json:"updated_at"`
	Comments   []Comment    `json:"comments"`
}

type ThreadSummary struct {
	ID            string       `json:"id"`
	DocumentID    string       `json:"document_id"`
	Anchor        Anchor       `json:"anchor"`
	Status        ThreadStatus `json:"status"`
	Author        string       `json:"author"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	CommentCount  int          `json:"comment_count"`
	LastCommentID string       `json:"last_comment_id,omitempty"`
	LastCommentBy string       `json:"last_comment_by,omitempty"`
	LastCommentAt *time.Time   `json:"last_comment_at,omitempty"`
}

type Annotation struct {
	ID         string         `json:"id"`
	DocumentID string         `json:"document_id"`
	Kind       string         `json:"kind"`
	Body       string         `json:"body,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	Anchor     *Anchor        `json:"anchor,omitempty"`
	Author     string         `json:"author"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type SuggestionOp struct {
	ID          string `json:"id"`
	Anchor      Anchor `json:"anchor"`
	Replacement string `json:"replacement"`
}

type SuggestionBatch struct {
	ID           string           `json:"id"`
	DocumentID   string           `json:"document_id"`
	BaseRevision int64            `json:"base_revision"`
	Status       SuggestionStatus `json:"status"`
	Author       string           `json:"author"`
	Rationale    string           `json:"rationale,omitempty"`
	Conflict     string           `json:"conflict,omitempty"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	Ops          []SuggestionOp   `json:"ops"`
}

type ActivityEvent struct {
	ID         string         `json:"id"`
	DocumentID string         `json:"document_id"`
	Type       string         `json:"type"`
	Actor      string         `json:"actor"`
	Payload    map[string]any `json:"payload,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type ImportRecord struct {
	ID           string         `json:"id"`
	DocumentID   string         `json:"document_id"`
	SourcePath   string         `json:"source_path"`
	SourceFormat DocumentFormat `json:"source_format"`
	Warnings     []string       `json:"warnings,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}
