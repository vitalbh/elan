// Package llm runs single-turn, tool-less LLM calls with structured output.
// The only implementation drives the Claude Code CLI (`claude -p`), so calls
// share the owner's subscription instead of a paid API.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Request is one single-turn call.
type Request struct {
	System string
	Prompt string
	// Model is a model alias or full name ("sonnet", "haiku", ...). Empty
	// leaves the CLI default.
	Model string
	// Schema is the JSON Schema the answer must follow.
	Schema json.RawMessage
}

// Runner answers a Request with a JSON document valid against its Schema.
type Runner interface {
	JSON(ctx context.Context, req Request) ([]byte, error)
}

// LimitError reports that the subscription hit a usage limit (session,
// weekly, ...). ResetAt is when it resets, zero when the message did not say
// or could not be parsed. A scheduler waits until ResetAt and retries.
type LimitError struct {
	Message string
	ResetAt time.Time
}

func (e *LimitError) Error() string {
	if e.ResetAt.IsZero() {
		return "limite de uso do Claude: " + e.Message
	}
	return fmt.Sprintf("limite de uso do Claude até %s: %s", e.ResetAt.Format(time.RFC3339), e.Message)
}

// IsLimit reports whether err is (or wraps) a *LimitError and returns it.
func IsLimit(err error) (*LimitError, bool) {
	var le *LimitError
	ok := errors.As(err, &le)
	return le, ok
}
