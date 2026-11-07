package flag

import (
	"strings"

	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/utils"
)

const (
	// AudienceStatusHealthy means every audience lookup of the evaluation was answered by the store.
	AudienceStatusHealthy = "HEALTHY"
	// AudienceStatusStoreError means at least one audience lookup failed (or is missing).
	AudienceStatusStoreError = "STORE_ERROR"
	// AudienceStatusNotConfigured means the flag uses audiences but no membership data was provided.
	AudienceStatusNotConfigured = "NOT_CONFIGURED"
	// AudienceStatusMetadataKey is the resolution metadata key carrying the audience status.
	// It never carries member ids or attribute values.
	AudienceStatusMetadataKey = "gofeatureflag_audienceStatus"
)

// AudienceLookup is one membership question: is Value a member of the audience List?
type AudienceLookup struct {
	List  string
	Value string
}

// AudienceResult is the answer to an AudienceLookup.
// When Err is true the store could not answer and Member must be ignored.
type AudienceResult struct {
	Member bool
	Err    bool
}

// AudienceMembership holds the answers of the audience store for an evaluation.
// A nil map means that audiences are not configured.
type AudienceMembership map[AudienceLookup]AudienceResult

// audienceStatusRank orders the statuses, the worst one wins when merging.
func audienceStatusRank(s string) int {
	switch s {
	case AudienceStatusNotConfigured:
		return 3
	case AudienceStatusStoreError:
		return 2
	case AudienceStatusHealthy:
		return 1
	default:
		return 0
	}
}

// worstAudienceStatus returns the worst of both statuses
// (NOT_CONFIGURED > STORE_ERROR > HEALTHY > "").
func worstAudienceStatus(a, b string) string {
	if audienceStatusRank(b) > audienceStatusRank(a) {
		return b
	}
	return a
}

// AudienceTerms returns the distinct audience terms used by the rules, the default rule
// and the scheduled steps of the flag. Rules with a query that does not compile are ignored.
func (f *InternalFlag) AudienceTerms() []AudienceTerm {
	if f == nil {
		return nil
	}
	var terms []AudienceTerm
	seen := map[string]struct{}{}
	collect := func(r *Rule) {
		if r == nil || r.Query == nil || !mayContainAudienceRaw(*r.Query) {
			return
		}
		q := r.GetTrimmedQuery()
		if q == "" {
			return
		}
		c, err := CompileAudienceQuery(q, r.GetQueryFormat())
		if err != nil {
			return
		}
		for _, t := range c.Terms {
			if _, ok := seen[t.Key]; ok {
				continue
			}
			seen[t.Key] = struct{}{}
			terms = append(terms, t)
		}
	}
	collectFlag := func(fl *InternalFlag) {
		if fl.Rules != nil {
			for i := range *fl.Rules {
				collect(&(*fl.Rules)[i])
			}
		}
		collect(fl.DefaultRule)
	}
	collectFlag(f)
	if f.Scheduled != nil {
		for i := range *f.Scheduled {
			collectFlag(&(*f.Scheduled)[i].InternalFlag)
		}
	}
	return terms
}

// hasAudience tells if any rule (including scheduled steps) uses an audience.
// It does not allocate when the flag has no audience.
func (f *InternalFlag) hasAudience() bool {
	if f == nil {
		return false
	}
	if rulesHaveAudience(f.Rules, f.DefaultRule) {
		return true
	}
	if f.Scheduled != nil {
		for i := range *f.Scheduled {
			step := &(*f.Scheduled)[i]
			if rulesHaveAudience(step.Rules, step.DefaultRule) {
				return true
			}
		}
	}
	return false
}

// mayContainAudienceRaw is an allocation-free pre-check on the raw (untrimmed) query: false
// guarantees that the query has no audience term and no reserved prefix.
// The audience keyword is searched case-insensitively (inAudience is covered too).
func mayContainAudienceRaw(q string) bool {
	return containsFoldASCII(q, nikunjyAudienceKeyword) || strings.Contains(q, AudienceAttributePrefix)
}

// containsFoldASCII reports whether s contains lowerSubstr (lower case ASCII), ignoring ASCII case.
func containsFoldASCII(s, lowerSubstr string) bool {
	n := len(lowerSubstr)
	for i := 0; i+n <= len(s); i++ {
		j := 0
		for ; j < n; j++ {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != lowerSubstr[j] {
				break
			}
		}
		if j == n {
			return true
		}
	}
	return false
}

func rulesHaveAudience(rules *[]Rule, def *Rule) bool {
	has := func(r *Rule) bool {
		if r.Query == nil || !mayContainAudienceRaw(*r.Query) {
			return false
		}
		q := r.GetTrimmedQuery()
		if q == "" {
			return false
		}
		c, err := CompileAudienceQuery(q, r.GetQueryFormat())
		return err == nil && c.HasAudience()
	}
	if rules != nil {
		for i := range *rules {
			if has(&(*rules)[i]) {
				return true
			}
		}
	}
	return def != nil && has(def)
}

// stripAudienceKeys removes the reserved attributes from m, they can only be injected by the evaluation.
func stripAudienceKeys(m map[string]any) {
	for k := range m {
		if strings.HasPrefix(k, AudienceAttributePrefix) {
			delete(m, k)
		}
	}
}

// AudienceQueryMap returns the map the query engines read from during the evaluation: the
// evaluation context (nil is an empty context, as in InternalFlag.Value) overlaid with the
// enrichment (the enrichment wins over the request attributes, as in InternalFlag.Value), then
// the targeting key attributes, without the reserved audience attributes. The context is not
// modified. The resolver reads the audience attributes through this map, so that it and the
// evaluation always look up the same value.
func AudienceQueryMap(ctx ffcontext.Context, enrichment map[string]any) map[string]any {
	if ctx == nil {
		ctx = ffcontext.NewEvaluationContext("")
	}
	custom := ctx.GetCustom()
	m := make(map[string]any, len(custom)+len(enrichment)+3)
	for k, v := range custom {
		m[k] = v
	}
	for k, v := range enrichment {
		m[k] = v
	}
	m["anonymous"] = ctx.IsAnonymous()
	m["key"] = ctx.GetKey()
	m["targetingKey"] = ctx.GetKey()
	stripAudienceKeys(m)
	return m
}

// AudienceContextMap returns the evaluation context as the map used by the query engines,
// without the reserved audience attributes (see AudienceQueryMap for the enriched variant).
func AudienceContextMap(ctx ffcontext.Context) map[string]any {
	return AudienceQueryMap(ctx, nil)
}

// AudienceAttributeValue returns the string value of the attribute (a dotted path such as
// "targetingKey" or "user.id") of the context map. It reports false when the attribute is
// missing or is not a string: the audience term is then unknown. The evaluation uses this
// same helper, so a resolver and the evaluation always agree on the looked-up value.
func AudienceAttributeValue(ctxMap map[string]any, attribute string) (string, bool) {
	raw, err := utils.GetNestedFieldValue(ctxMap, attribute)
	if err != nil {
		return "", false
	}
	value, ok := raw.(string)
	return value, ok
}
