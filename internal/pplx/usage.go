package pplx

import (
	"context"
	"fmt"

	"github.com/spacingmind/perplexity-proxy-go/internal/spec"
	"github.com/spacingmind/perplexity-proxy-go/internal/transport"
)

// SourceLimit is the monthly cap for one source (web, scholar, connectors).
type SourceLimit struct {
	SourceID     string `json:"source_id"`
	MonthlyLimit *int   `json:"monthly_limit"`
	Remaining    *int   `json:"remaining"`
}

// Unlimited reports whether the source has no monthly cap.
func (s SourceLimit) Unlimited() bool { return s.MonthlyLimit == nil }

// RateLimits is the typed view of the rate-limit/all response, ported from
// the reference rate_limits.py. Unknown fields are ignored.
type RateLimits struct {
	RemainingPro             int            `json:"remaining_pro"`
	RemainingResearch        int            `json:"remaining_research"`
	RemainingLabs            int            `json:"remaining_labs"`
	RemainingAgenticResearch int            `json:"remaining_agentic_research"`
	ModelSpecificLimits      map[string]any `json:"model_specific_limits"`
	SourceLimits             []SourceLimit  `json:"source_limits"`
}

// Usage fetches and parses the rate-limits endpoint.
func Usage(ctx context.Context, t transport.Client, sp *spec.Spec) (*RateLimits, error) {
	var raw struct {
		RemainingPro             int            `json:"remaining_pro"`
		RemainingResearch        int            `json:"remaining_research"`
		RemainingLabs            int            `json:"remaining_labs"`
		RemainingAgenticResearch int            `json:"remaining_agentic_research"`
		ModelSpecificLimits      map[string]any `json:"model_specific_limits"`
		Sources                  struct {
			SourceToLimit map[string]struct {
				MonthlyLimit *int `json:"monthly_limit"`
				Remaining    *int `json:"remaining"`
			} `json:"source_to_limit"`
		} `json:"sources"`
	}
	if err := t.GetJSON(ctx, sp.Endpoints.RateLimits, &raw); err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	out := &RateLimits{
		RemainingPro:             raw.RemainingPro,
		RemainingResearch:        raw.RemainingResearch,
		RemainingLabs:            raw.RemainingLabs,
		RemainingAgenticResearch: raw.RemainingAgenticResearch,
		ModelSpecificLimits:      raw.ModelSpecificLimits,
	}
	for id, l := range raw.Sources.SourceToLimit {
		ml, rm := l.MonthlyLimit, l.Remaining
		out.SourceLimits = append(out.SourceLimits, SourceLimit{SourceID: id, MonthlyLimit: ml, Remaining: rm})
	}
	return out, nil
}
