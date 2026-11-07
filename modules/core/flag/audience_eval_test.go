package flag_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/modules/core/testutils/testconvert"
)

func audVariations() *map[string]*any {
	on, off := any("on"), any("off")
	return &map[string]*any{"on": &on, "off": &off}
}

func audFlag(rules ...flag.Rule) *flag.InternalFlag {
	return &flag.InternalFlag{
		Variations:  audVariations(),
		Rules:       &rules,
		DefaultRule: &flag.Rule{VariationResult: testconvert.String("off")},
	}
}

func audRule(name, query, variation string) flag.Rule {
	return flag.Rule{
		Name:            testconvert.String(name),
		Query:           testconvert.String(query),
		VariationResult: testconvert.String(variation),
	}
}

func member(list, value string) flag.AudienceMembership {
	return flag.AudienceMembership{
		{List: list, Value: value}: {Member: true},
	}
}

func TestAudienceEvaluation(t *testing.T) {
	jsonQuery := `{"inAudience":[{"var":"targetingKey"},"beta"]}`
	onMatch := audRule("r", `targetingKey in audience("blocklist")`, "off")
	onMatch.OnError = testconvert.String("match")
	deny := audRule("deny", `targetingKey in audience("blocklist")`, "off")
	deny.OnError = testconvert.String("match")
	allow := audRule("allow", `targetingKey in audience("beta")`, "on")
	notRule := audRule("excl", `not (targetingKey in audience("churned")) and country eq "US"`, "on")

	tests := []struct {
		name       string
		flag       *flag.InternalFlag
		ctx        ffcontext.Context
		enrichment map[string]any
		membership flag.AudienceMembership
		wantVar    string
		wantReason flag.ResolutionReason
		wantStatus string // "" = key must be absent
	}{
		{
			name: "member matches", flag: audFlag(allow), ctx: ffcontext.NewEvaluationContext("u1"),
			membership: member("beta", "u1"), wantVar: "on", wantReason: flag.ReasonTargetingMatch,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "non member falls to default", flag: audFlag(allow), ctx: ffcontext.NewEvaluationContext("u2"),
			membership: flag.AudienceMembership{{List: "beta", Value: "u2"}: {}},
			wantVar:    "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "multi name OR",
			flag: audFlag(audRule("r", `targetingKey in audience("a", "b")`, "on")),
			ctx:  ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{
				{List: "a", Value: "u1"}: {Member: false},
				{List: "b", Value: "u1"}: {Member: true},
			},
			wantVar: "on", wantReason: flag.ReasonTargetingMatch, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "exclusion with country", flag: audFlag(notRule),
			ctx: ffcontext.NewEvaluationContextBuilder("u1").AddCustom("country", "US").Build(),
			membership: flag.AudienceMembership{
				{List: "churned", Value: "u1"}: {Member: false},
			},
			wantVar: "on", wantReason: flag.ReasonTargetingMatch, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "exclusion blocks member", flag: audFlag(notRule),
			ctx:        ffcontext.NewEvaluationContextBuilder("u1").AddCustom("country", "US").Build(),
			membership: member("churned", "u1"), wantVar: "off", wantReason: flag.ReasonDefault,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "nil membership skips rule, NOT_CONFIGURED", flag: audFlag(allow),
			ctx: ffcontext.NewEvaluationContext("u1"), membership: nil,
			wantVar: "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusNotConfigured,
		},
		{
			name: "store error skips rule", flag: audFlag(allow), ctx: ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{{List: "beta", Value: "u1"}: {Err: true}},
			wantVar:    "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "exclusion rule never lets user through on error", flag: audFlag(notRule),
			ctx:        ffcontext.NewEvaluationContextBuilder("u1").AddCustom("country", "US").Build(),
			membership: flag.AudienceMembership{{List: "churned", Value: "u1"}: {Err: true}},
			wantVar:    "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "missing lookup is a store error", flag: audFlag(allow), ctx: ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{}, wantVar: "off", wantReason: flag.ReasonDefault,
			wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "onError match deny rule matches on store error", flag: audFlag(deny, allow),
			ctx:        ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{{List: "blocklist", Value: "u1"}: {Err: true}},
			wantVar:    "off", wantReason: flag.ReasonTargetingMatch, wantStatus: flag.AudienceStatusStoreError,
		},
		{
			name: "onError match with nil membership", flag: audFlag(onMatch),
			ctx: ffcontext.NewEvaluationContext("u1"), membership: nil,
			wantVar: "off", wantReason: flag.ReasonTargetingMatch, wantStatus: flag.AudienceStatusNotConfigured,
		},
		{
			name: "spoofed custom attribute is ignored",
			flag: audFlag(allow),
			ctx: ffcontext.NewEvaluationContextBuilder("u1").
				AddCustom(flag.AudienceAttributeKey("beta", "targetingKey"), true).Build(),
			membership: flag.AudienceMembership{{List: "beta", Value: "u1"}: {}},
			wantVar:    "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "spoofed enrichment is ignored", flag: audFlag(allow), ctx: ffcontext.NewEvaluationContext("u1"),
			enrichment: map[string]any{flag.AudienceAttributeKey("beta", "targetingKey"): true},
			membership: flag.AudienceMembership{{List: "beta", Value: "u1"}: {}},
			wantVar:    "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "dotted attribute",
			flag: audFlag(audRule("r", `user.id in audience("beta")`, "on")),
			ctx: ffcontext.NewEvaluationContextBuilder("u1").
				AddCustom("user", map[string]any{"id": "abc"}).Build(),
			membership: member("beta", "abc"), wantVar: "on", wantReason: flag.ReasonTargetingMatch,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name:       "other attribute (email)",
			flag:       audFlag(audRule("r", `email in audience("beta")`, "on")),
			ctx:        ffcontext.NewEvaluationContextBuilder("u1").AddCustom("email", "a@b.c").Build(),
			membership: member("beta", "a@b.c"), wantVar: "on", wantReason: flag.ReasonTargetingMatch,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "missing attribute is unknown", flag: audFlag(audRule("r", `email in audience("beta")`, "on")),
			ctx:        ffcontext.NewEvaluationContext("u1"),
			membership: member("beta", "a@b.c"), wantVar: "off", wantReason: flag.ReasonDefault,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name:       "non string attribute is unknown",
			flag:       audFlag(audRule("r", `age in audience("beta")`, "on")),
			ctx:        ffcontext.NewEvaluationContextBuilder("u1").AddCustom("age", 3).Build(),
			membership: member("beta", "3"), wantVar: "off", wantReason: flag.ReasonDefault,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "jsonlogic inAudience", flag: audFlag(audRule("r", jsonQuery, "on")),
			ctx:        ffcontext.NewEvaluationContext("u1"),
			membership: member("beta", "u1"), wantVar: "on", wantReason: flag.ReasonTargetingMatch,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "jsonlogic non member", flag: audFlag(audRule("r", jsonQuery, "on")),
			ctx:        ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{{List: "beta", Value: "u1"}: {}}, wantVar: "off", wantReason: flag.ReasonDefault,
			wantStatus: flag.AudienceStatusHealthy,
		},
		{
			name: "no audience in flag: no status", flag: audFlag(audRule("r", `key eq "u1"`, "on")),
			ctx:        ffcontext.NewEvaluationContext("u1"),
			membership: member("beta", "u1"), wantVar: "on", wantReason: flag.ReasonTargetingMatch,
		},
		{
			name: "invalid audience query never matches",
			flag: audFlag(audRule("r", `targetingKey in audience("BAD")`, "on")),
			ctx:  ffcontext.NewEvaluationContext("u1"), membership: member("BAD", "u1"),
			wantVar: "off", wantReason: flag.ReasonDefault,
		},
		{
			name: "status not leaked to later rules: worst wins",
			flag: audFlag(
				audRule("a", `targetingKey in audience("x")`, "on"),
				audRule("b", `targetingKey in audience("y")`, "on"),
			),
			ctx: ffcontext.NewEvaluationContext("u1"),
			membership: flag.AudienceMembership{
				{List: "x", Value: "u1"}: {Member: false},
				{List: "y", Value: "u1"}: {Err: true},
			},
			wantVar: "off", wantReason: flag.ReasonDefault, wantStatus: flag.AudienceStatusStoreError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var enrichment map[string]any
			if tt.enrichment != nil {
				enrichment = map[string]any{}
				for k, v := range tt.enrichment {
					enrichment[k] = v
				}
			}
			got, details := tt.flag.Value("f", tt.ctx, flag.Context{
				EvaluationContextEnrichment: enrichment,
				AudienceMembership:          tt.membership,
			})
			assert.Equal(t, tt.wantVar, got)
			assert.Equal(t, tt.wantVar, details.Variant)
			assert.Equal(t, tt.wantReason, details.Reason)
			status, present := details.Metadata[flag.AudienceStatusMetadataKey]
			if tt.wantStatus == "" {
				assert.False(t, present)
			} else {
				assert.Equal(t, tt.wantStatus, status)
			}
		})
	}
}

func TestAudienceEvaluation_SharedMetadataNotMutated(t *testing.T) {
	f := audFlag(audRule("r", `targetingKey in audience("beta")`, "on"))
	f.Metadata = &map[string]any{"k": "v"}
	_, d := f.Value("f", ffcontext.NewEvaluationContext("u1"),
		flag.Context{AudienceMembership: member("beta", "u1")})
	assert.Equal(t, flag.AudienceStatusHealthy, d.Metadata[flag.AudienceStatusMetadataKey])
	assert.Equal(t, "v", d.Metadata["k"])
	assert.Equal(t, map[string]any{"k": "v"}, *f.Metadata)
}

func TestAudienceCacheable(t *testing.T) {
	tests := []struct {
		name string
		flag *flag.InternalFlag
		want bool
	}{
		{"audience flag is not cacheable", audFlag(audRule("r", `targetingKey in audience("a")`, "on")), false},
		{"normal flag is cacheable", audFlag(audRule("r", `key eq "u1"`, "on")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, d := tt.flag.Value("f", ffcontext.NewEvaluationContext("u1"),
				flag.Context{AudienceMembership: member("a", "u1")})
			assert.Equal(t, tt.want, d.Cacheable)
		})
	}
}

func TestAudienceScheduledStep(t *testing.T) {
	f := audFlag(audRule("r", `key eq "nobody"`, "on"))
	stepDate := time.Now().Add(-time.Hour)
	f.Scheduled = &[]flag.ScheduledStep{{
		InternalFlag: flag.InternalFlag{
			Rules: &[]flag.Rule{audRule("beta", `targetingKey in audience("beta")`, "on")},
		},
		Date: &stepDate,
	}}
	got, d := f.Value("f", ffcontext.NewEvaluationContext("u1"),
		flag.Context{AudienceMembership: member("beta", "u1")})
	assert.Equal(t, "on", got)
	assert.Equal(t, flag.ReasonTargetingMatch, d.Reason)
	assert.Equal(t, flag.AudienceStatusHealthy, d.Metadata[flag.AudienceStatusMetadataKey])
	assert.False(t, d.Cacheable)
}

func TestAudienceTerms(t *testing.T) {
	tests := []struct {
		name string
		flag *flag.InternalFlag
		want []string // lists
	}{
		{"nil flag", nil, nil},
		{"no audience", audFlag(audRule("r", `key eq "a"`, "on")), nil},
		{
			"rules, default and scheduled deduped",
			func() *flag.InternalFlag {
				f := audFlag(
					audRule("a", `targetingKey in audience("a")`, "on"),
					audRule("b", `targetingKey in audience("a") or targetingKey in audience("b")`, "on"),
					audRule("bad", `targetingKey in audience("BAD")`, "on"),
				)
				f.DefaultRule.Query = testconvert.String(`targetingKey in audience("d")`)
				f.Scheduled = &[]flag.ScheduledStep{{
					InternalFlag: flag.InternalFlag{
						Rules:       &[]flag.Rule{audRule("c", `{"inAudience":[{"var":"targetingKey"},"c"]}`, "on")},
						DefaultRule: &flag.Rule{Query: testconvert.String(`targetingKey in audience("a")`)},
					},
				}}
				return f
			}(),
			[]string{"a", "b", "d", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lists []string
			for _, term := range tt.flag.AudienceTerms() {
				lists = append(lists, term.List)
			}
			assert.Equal(t, tt.want, lists)
		})
	}
}

func TestAudienceValidation(t *testing.T) {
	vars := *audVariations()
	withOnError := func(r flag.Rule, v string) flag.Rule { r.OnError = testconvert.String(v); return r }
	tests := []struct {
		name        string
		rule        flag.Rule
		defaultRule bool
		wantErr     string
	}{
		{"valid audience rule", audRule("r", `targetingKey in audience("beta")`, "on"), false, ""},
		{"valid jsonlogic", audRule("r", `{"inAudience":[{"var":"targetingKey"},"beta"]}`, "on"), false, ""},
		{"valid onError match", withOnError(audRule("r", `targetingKey in audience("beta")`, "on"), "match"), false, ""},
		{"invalid onError", withOnError(audRule("r", `targetingKey in audience("beta")`, "on"), "foo"), false, "onError"},
		{"bad name", audRule("r", `targetingKey in audience("Bad")`, "on"), false, "invalid audience name"},
		{"spoof prefix", audRule("r", `goff_aud_x eq true`, "on"), false, "reserved prefix"},
		{"defaultRule with audience", audRule("r", `targetingKey in audience("beta")`, "on"), true, "defaultRule"},
		{"defaultRule with bad audience", audRule("r", `targetingKey in audience("Bad")`, "on"), true, "invalid audience name"},
		{"defaultRule without query", flag.Rule{VariationResult: testconvert.String("on")}, true, ""},
		{
			"disabled rule skips validation",
			func() flag.Rule {
				r := audRule("r", `targetingKey in audience("Bad")`, "on")
				r.Disable = testconvert.Bool(true)
				return r
			}(), false, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.IsValid(tt.defaultRule, vars)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}

	t.Run("valid flag", func(t *testing.T) {
		assert.NoError(t, audFlag(audRule("r", `targetingKey in audience("beta")`, "on")).IsValid())
	})
}

func TestRuleMergeOnError(t *testing.T) {
	r := audRule("r", `targetingKey in audience("beta")`, "on")
	assert.Equal(t, "skip", r.GetOnError())
	r.MergeRules(flag.Rule{OnError: testconvert.String("match")})
	assert.Equal(t, "match", r.GetOnError())
}
