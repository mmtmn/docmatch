package estimate

import (
	"context"
	"fmt"
)

// FallbackModelPrices is the static table seeded into model_prices when it
// is empty (dev and first boot). Values are cents per million tokens and
// are placeholders: the price fetch job replaces them with the provider's
// daily list, which is why every estimate records pricing_version.
func FallbackModelPrices() []ModelPrice {
	return []ModelPrice{
		{Model: "anthropic/claude-haiku-5.5", InputCentsPerMTok: 100, OutputCentsPerMTok: 500, Source: "fallback"},
		{Model: "anthropic/claude-sonnet-5.5", InputCentsPerMTok: 300, OutputCentsPerMTok: 1500, Source: "fallback"},
		{Model: "anthropic/claude-opus-5.5", InputCentsPerMTok: 1500, OutputCentsPerMTok: 7500, Source: "fallback"},
		{Model: "openai/gpt-6.1-sol", InputCentsPerMTok: 250, OutputCentsPerMTok: 1000, Source: "fallback"},
		{Model: "google/gemini-4-argon", InputCentsPerMTok: 125, OutputCentsPerMTok: 500, Source: "fallback"},
		{Model: "openai/text-embedding-4", InputCentsPerMTok: 2, OutputCentsPerMTok: 0, Source: "fallback"},
	}
}

// EnsureModelPrices seeds the fallback table when model_prices is empty.
func EnsureModelPrices(ctx context.Context, store Store) error {
	if err := store.SeedModelPrices(ctx, FallbackModelPrices()); err != nil {
		return fmt.Errorf("estimate: semear preços de modelo: %w", err)
	}
	return nil
}
