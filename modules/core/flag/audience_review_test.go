package flag_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/modules/core/testutils/testconvert"
)

func TestAudienceDisabledRuleDoesNoAudienceWork(t *testing.T) {
	disabled := audRule("r", `targetingKey in audience("a")`, "on")
	disabled.Disable = testconvert.Bool(true)
	tests := []struct {
		name       string
		membership flag.AudienceMembership
	}{
		{"nil membership", nil},
		{"store error", flag.AudienceMembership{{List: "a", Value: "u1"}: {Err: true}}},
		{"member", member("a", "u1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, d := audFlag(disabled).Value("f", ffcontext.NewEvaluationContext("u1"),
				flag.Context{AudienceMembership: tt.membership})
			assert.Equal(t, "off", got)
			assert.Equal(t, flag.ReasonDefault, d.Reason)
			_, present := d.Metadata[flag.AudienceStatusMetadataKey]
			assert.False(t, present)
		})
	}
}

func TestAudienceUnknownTermsSemantics(t *testing.T) {
	storeErr := func(list, v string) flag.AudienceMembership {
		return flag.AudienceMembership{{List: list, Value: v}: {Err: true}}
	}
	withOnError := func(r flag.Rule, v string) flag.Rule { r.OnError = testconvert.String(v); return r }
	pro := ffcontext.NewEvaluationContextBuilder("u1").AddCustom("plan", "pro").AddCustom("country", "US").Build()
	free := ffcontext.NewEvaluationContextBuilder("u1").AddCustom("plan", "free").AddCustom("country", "US").Build()
	fr := ffcontext.NewEvaluationContextBuilder("u1").AddCustom("country", "FR").Build()
	orRule := audRule("or", `plan eq "pro" or targetingKey in audience("a")`, "on")
	andRule := audRule("and", `country eq "FR" and targetingKey in audience("a")`, "on")
	notRule := audRule("not", `not (targetingKey in audience("churned"))`, "on")
	jsonOr := audRule("jor", `{"or":[{"==":[{"var":"plan"},"pro"]},{"inAudience":[{"var":"targetingKey"},"a"]}]}`, "on")
	manyTerms := audRule("many",
		`targetingKey in audience("a") or targetingKey in audience("b") or targetingKey in audience("c") `+
			`or targetingKey in audience("d") or targetingKey in audience("e")`, "on")
	manyAll := audRule("many",
		`plan eq "x" and (targetingKey in audience("a") or targetingKey in audience("b") or `+
			`targetingKey in audience("c") or targetingKey in audience("d") or targetingKey in audience("e"))`, "on")

	tests := []struct {
		name       string
		flag       *flag.InternalFlag
		ctx        ffcontext.Context
		membership flag.AudienceMembership
		wantVar    string
	}{
		{"or short-circuit pro user matches on store error", audFlag(orRule), pro, storeErr("a", "u1"), "on"},
		{"or short-circuit pro user matches with nil membership", audFlag(orRule), pro, nil, "on"},
		{"or free user depends on unknown: skipped", audFlag(orRule), free, storeErr("a", "u1"), "off"},
		{"or free user, onError match", audFlag(withOnError(orRule, "match")), free, storeErr("a", "u1"), "on"},
		{"jsonlogic or pro user matches on store error", audFlag(jsonOr), pro, storeErr("a", "u1"), "on"},
		{"and with other country does not match on error", audFlag(andRule), pro, storeErr("a", "u1"), "off"},
		{
			"and with other country does not match even with onError match",
			audFlag(withOnError(andRule, "match")), pro, storeErr("a", "u1"), "off",
		},
		{"and country FR depends on unknown: skipped", audFlag(andRule), fr, storeErr("a", "u1"), "off"},
		{"and country FR onError match", audFlag(withOnError(andRule, "match")), fr, storeErr("a", "u1"), "on"},
		{"not still skipped on store error (fail closed)", audFlag(notRule), pro, storeErr("churned", "u1"), "off"},
		{
			"not with onError match: unknown is member so it does not match",
			audFlag(withOnError(notRule, "match")), pro, storeErr("churned", "u1"), "off",
		},
		{"deny onError match still matches", audFlag(withOnError(audRule("deny", `targetingKey in audience("bl")`, "off"), "match"),
			audRule("allow", `targetingKey in audience("a")`, "on")), pro, storeErr("bl", "u1"), "off"},
		{"more than 4 unknown terms goes to onError skip", audFlag(manyTerms), pro, nil, "off"},
		{"more than 4 unknown terms goes to onError match", audFlag(withOnError(manyTerms, "match")), pro, nil, "on"},
		{"more than 4 unknown terms not enumerated, outcome independent is still onError", audFlag(manyAll), pro, nil, "off"},
		{
			"known terms keep their value: known member + unknown",
			audFlag(audRule("k", `targetingKey in audience("a") and targetingKey in audience("b")`, "on")),
			pro,
			flag.AudienceMembership{{List: "a", Value: "u1"}: {Member: false}, {List: "b", Value: "u1"}: {Err: true}},
			"off",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := tt.flag.Value("f", tt.ctx, flag.Context{AudienceMembership: tt.membership})
			assert.Equal(t, tt.wantVar, got)
		})
	}

	t.Run("status still reflects the lookups when outcome is independent", func(t *testing.T) {
		_, d := audFlag(orRule).Value("f", pro, flag.Context{AudienceMembership: storeErr("a", "u1")})
		assert.Equal(t, flag.AudienceStatusStoreError, d.Metadata[flag.AudienceStatusMetadataKey])
	})
}

func TestAudienceCacheabilityAndRawCheck(t *testing.T) {
	step := func(query string) *[]flag.ScheduledStep {
		d := time.Now().Add(-time.Hour)
		return &[]flag.ScheduledStep{{
			InternalFlag: flag.InternalFlag{Rules: &[]flag.Rule{audRule("s", query, "on")}},
			Date:         &d,
		}}
	}
	tests := []struct {
		name string
		flag *flag.InternalFlag
		want int // number of audience terms
	}{
		{"uppercase keyword", audFlag(audRule("r", `targetingKey in AUDIENCE("a")`, "on")), 1},
		{"jsonlogic", audFlag(audRule("r", `  {"inAudience":[{"var":"targetingKey"},"a"]}`, "on")), 1},
		{"no audience", audFlag(audRule("r", `key eq "u1"`, "on")), 0},
		{"attribute named like the keyword is not a term", audFlag(audRule("r", `audience eq "x"`, "on")), 0},
		{"in scheduled step", func() *flag.InternalFlag {
			f := audFlag()
			f.Scheduled = step(`targetingKey in audience("a")`)
			return f
		}(), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, tt.flag.AudienceTerms(), tt.want)
		})
	}
}

func TestAudienceScheduledStepValidation(t *testing.T) {
	withOnError := func(r flag.Rule, v string) flag.Rule { r.OnError = testconvert.String(v); return r }
	disabled := audRule("r", `targetingKey in audience("Bad")`, "on")
	disabled.Disable = testconvert.Bool(true)
	tests := []struct {
		name        string
		rules       []flag.Rule
		defaultRule *flag.Rule
		wantErr     string
	}{
		{"valid audience rule", []flag.Rule{audRule("r", `targetingKey in audience("a")`, "on")}, nil, ""},
		{"bad audience name", []flag.Rule{audRule("r", `targetingKey in audience("Bad")`, "on")}, nil, "invalid audience name"},
		{"spoofed prefix", []flag.Rule{audRule("r", `goff_aud_x eq true`, "on")}, nil, "reserved prefix"},
		{"mixed case keyword", []flag.Rule{audRule("r", `x in Audience("a")`, "on")}, nil, "invalid audience keyword"},
		{
			"audience in step defaultRule",
			nil, &flag.Rule{Query: testconvert.String(`targetingKey in audience("a")`), VariationResult: testconvert.String("on")},
			"defaultRule",
		},
		{
			"valid step defaultRule",
			nil, &flag.Rule{VariationResult: testconvert.String("on")}, "",
		},
		{"invalid onError", []flag.Rule{withOnError(audRule("r", `key eq "x"`, "on"), "nope")}, nil, "onError"},
		{"valid onError", []flag.Rule{withOnError(audRule("r", `targetingKey in audience("a")`, "on"), "match")}, nil, ""},
		{"disabled rule skipped", []flag.Rule{disabled}, nil, ""},
		{
			"non audience query is not validated in steps (unchanged behavior)",
			[]flag.Rule{audRule("r", `this is ((( not valid`, "on")},
			nil, "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := audFlag(audRule("base", `key eq "u"`, "on"))
			d := time.Now().Add(time.Hour)
			step := flag.ScheduledStep{InternalFlag: flag.InternalFlag{DefaultRule: tt.defaultRule}, Date: &d}
			if tt.rules != nil {
				step.Rules = &tt.rules
			}
			f.Scheduled = &[]flag.ScheduledStep{step}
			err := f.IsValid()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
