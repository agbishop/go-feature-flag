package flag_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
)

func TestAudienceQueryMap(t *testing.T) {
	reserved := flag.AudienceAttributeKey("l", "a")
	tests := []struct {
		name       string
		ctx        ffcontext.Context
		enrichment map[string]any
		want       map[string]any
	}{
		{
			name: "nil context is an empty context", ctx: nil,
			want: map[string]any{"anonymous": false, "key": "", "targetingKey": ""},
		},
		{
			name: "request attributes", ctx: ffcontext.NewEvaluationContextBuilder("u").AddCustom("a", "1").Build(),
			want: map[string]any{"anonymous": false, "key": "u", "targetingKey": "u", "a": "1"},
		},
		{
			name: "enrichment only", ctx: ffcontext.NewEvaluationContext("u"), enrichment: map[string]any{"env": "prod"},
			want: map[string]any{"anonymous": false, "key": "u", "targetingKey": "u", "env": "prod"},
		},
		{
			name:       "enrichment wins over the request",
			ctx:        ffcontext.NewEvaluationContextBuilder("u").AddCustom("a", "1").Build(),
			enrichment: map[string]any{"a": "2"},
			want:       map[string]any{"anonymous": false, "key": "u", "targetingKey": "u", "a": "2"},
		},
		{
			name:       "reserved attributes are stripped from both",
			ctx:        ffcontext.NewEvaluationContextBuilder("u").AddCustom(reserved, true).Build(),
			enrichment: map[string]any{reserved + "x": true},
			want:       map[string]any{"anonymous": false, "key": "u", "targetingKey": "u"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, flag.AudienceQueryMap(tt.ctx, tt.enrichment))
		})
	}
	t.Run("the context and the enrichment are not modified", func(t *testing.T) {
		c := ffcontext.NewEvaluationContextBuilder("u").AddCustom("a", "1").Build()
		flag.AudienceQueryMap(c, map[string]any{"a": "2"})
		assert.Equal(t, "1", c.GetCustom()["a"])
	})
}

// The evaluation and AudienceQueryMap must read the same value for an attribute.
func TestAudienceQueryMap_AgreesWithEvaluation(t *testing.T) {
	f := audFlag(audRule("r", `tenant in audience("big")`, "on"))
	tests := []struct {
		name       string
		custom     map[string]any
		enrichment map[string]any
	}{
		{"enrichment only", nil, map[string]any{"tenant": "big"}},
		{"enrichment overrides", map[string]any{"tenant": "small"}, map[string]any{"tenant": "big"}},
		{"request only", map[string]any{"tenant": "big"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := ffcontext.NewEvaluationContextBuilder("u")
			for k, v := range tt.custom {
				b.AddCustom(k, v)
			}
			ctx := b.Build()
			// what the resolver looks up
			value, ok := flag.AudienceAttributeValue(flag.AudienceQueryMap(ctx, tt.enrichment), "tenant")
			assert.True(t, ok)
			assert.Equal(t, "big", value)
			// and the evaluation finds the member
			v, d := f.Value("f", ctx, flag.Context{
				EvaluationContextEnrichment: tt.enrichment,
				AudienceMembership:          member("big", value),
			})
			assert.Equal(t, "on", v)
			assert.Equal(t, flag.ReasonTargetingMatch, d.Reason)
		})
	}
}

func TestAudienceContextMap(t *testing.T) {
	reserved := flag.AudienceAttributeKey("l", "a")
	tests := []struct {
		name string
		ctx  ffcontext.Context
		want map[string]any
	}{
		{
			name: "nil context is an empty context",
			ctx:  nil,
			want: map[string]any{"anonymous": false, "key": "", "targetingKey": ""},
		},
		{
			name: "request attributes",
			ctx:  ffcontext.NewEvaluationContextBuilder("u").AddCustom("a", "1").Build(),
			want: map[string]any{"anonymous": false, "key": "u", "targetingKey": "u", "a": "1"},
		},
		{
			name: "reserved attributes are stripped",
			ctx:  ffcontext.NewEvaluationContextBuilder("u").AddCustom(reserved, true).Build(),
			want: map[string]any{"anonymous": false, "key": "u", "targetingKey": "u"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, flag.AudienceContextMap(tt.ctx))
		})
	}
}
