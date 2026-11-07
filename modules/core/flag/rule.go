package flag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/diegoholiveira/jsonlogic/v3"
	"github.com/thomaspoignant/go-feature-flag/modules/core/ffcontext"
	"github.com/thomaspoignant/go-feature-flag/modules/core/internalerror"
	"github.com/thomaspoignant/go-feature-flag/modules/core/utils"
)

type QueryFormat = string

const (
	NikunjyQueryFormat   QueryFormat = "nikunjy"
	JSONLogicQueryFormat QueryFormat = "jsonlogic"
)

// Rule represents a rule applied by the flag.
type Rule struct {
	// Name is the name of the rule, this field is mandatory if you want
	// to update the rule during scheduled rollout
	Name *string `json:"name,omitempty" yaml:"name,omitempty" toml:"name,omitempty" jsonschema:"title=name,description=Name is the name of the rule. This field is mandatory if you want to update the rule during scheduled rollout."` // nolint: lll

	// Query represents the query used to target the audience of the flag.
	Query *string `json:"query,omitempty" yaml:"query,omitempty" toml:"query,omitempty" jsonschema:"title=query,description=The query that allow to check in the evaluation context match. Note: in the defaultRule field query is ignored."` // nolint: lll

	// VariationResult represents the variation name to use if the rule apply for the user.
	// In case we have a percentage field in the config VariationResult is ignored
	VariationResult *string `json:"variation,omitempty" yaml:"variation,omitempty" toml:"variation,omitempty" jsonschema:"title=variation,description=The variation name to use if the rule apply for the user. In case we have a percentage field in the config this field is ignored"` // nolint: lll

	// Percentages represents the percentage we should give to each variation.
	// example: variationA = 10%, variationB = 80%, variationC = 10%
	Percentages *map[string]float64 `json:"percentage,omitempty" yaml:"percentage,omitempty" toml:"percentage,omitempty" jsonschema:"title=percentage,description=Represents the percentage we should give to each variation."` // nolint: lll

	// ProgressiveRollout is your struct to configure a progressive rollout deployment of your flag.
	// It will allow you to ramp up the percentage of your flag over time.
	// You can decide at which percentage you starts with and at what percentage you ends with in your release ramp.
	// Before the start date we will serve the initial percentage and, after we will serve the end percentage.
	ProgressiveRollout *ProgressiveRollout `json:"progressiveRollout,omitempty" yaml:"progressiveRollout,omitempty" toml:"progressiveRollout,omitempty" jsonschema:"title=progressiveRollout,description=Configure a progressive rollout deployment of your flag."` // nolint: lll

	// Disable indicates that this rule is disabled.
	Disable *bool `json:"disable,omitempty" yaml:"disable,omitempty" toml:"disable,omitempty" jsonschema:"title=disable,description=Indicates that this rule is disabled."` // nolint: lll

	// OnError defines what happens when an audience used by the query cannot be resolved
	// (store error, audience not configured, missing attribute): skip (default) or match.
	OnError *string `json:"onError,omitempty" yaml:"onError,omitempty" toml:"onError,omitempty" jsonschema:"title=onError,description=Behavior when an audience of the query cannot be resolved. skip (default) means the rule does not match and match means unknown audiences are considered as member.,enum=skip,enum=match"` // nolint: lll
}

const (
	// OnErrorSkip makes the rule not match when an audience cannot be resolved (default).
	OnErrorSkip = "skip"
	// OnErrorMatch makes the rule consider unresolved audiences as member, when the outcome of the
	// query depends on them. This also applies when audiences are not configured (nil membership,
	// NOT_CONFIGURED, e.g. WASM / in-process evaluation): it is intentional, so a deny rule keeps
	// matching when the membership cannot be resolved.
	OnErrorMatch = "match"
)

// GetOnError returns the onError behavior of the rule, "skip" by default.
func (r *Rule) GetOnError() string {
	if r.OnError == nil || *r.OnError == "" {
		return OnErrorSkip
	}
	return *r.OnError
}

// RequiresBucketing checks if this rule requires a bucketing key for evaluation
func (r *Rule) RequiresBucketing() bool {
	// Rules with percentages need bucketing for distribution
	if r.Percentages != nil && len(r.GetPercentages()) > 0 {
		return true
	}

	// Rules with progressive rollout need bucketing
	if r.ProgressiveRollout != nil {
		return true
	}

	return false
}

// Evaluate is checking if the rule applies to for the user.
// If yes, it returns the variation you should use for this rule.
func (r *Rule) Evaluate(key string, ctx ffcontext.Context, flagName string, isDefault bool,
) (string, error) {
	return r.evaluate(key, &queryContext{ctx: ctx}, flagName, isDefault)
}

// evaluate is Evaluate reading the context through queryCtx, which the caller can share
// across the rules of a flag.
func (r *Rule) evaluate(key string, queryCtx *queryContext, flagName string, isDefault bool,
) (string, error) {
	ctx := queryCtx.ctx
	// Only require key if this rule needs bucketing
	if key == "" && r.RequiresBucketing() {
		return "", fmt.Errorf("evaluate Rule: no key for bucketing-required rule")
	}

	evaluationDate := DateFromContextOrDefault(ctx, time.Now())
	// check that we have an evaluation context
	if ctx == nil {
		return "", fmt.Errorf("evaluate Rule: no evaluation context")
	}

	// Check if the rule applies for this user
	// A disabled rule never applies: check it first so it does no query (nor audience) work.
	if !isDefault && (r.IsDisable() || !r.queryApplies(queryCtx)) {
		return "", &internalerror.RuleNotApplyError{Context: ctx}
	}
	if r.ProgressiveRollout != nil {
		if key == "" {
			return "", fmt.Errorf("progressive rollout requires a bucketing key")
		}
		return r.EvaluateProgressiveRollout(key, flagName, evaluationDate)
	}
	if r.Percentages != nil && len(r.GetPercentages()) > 0 {
		if key == "" {
			return "", fmt.Errorf("percentage rollout requires a bucketing key")
		}
		return r.EvaluatePercentageRollout(key, flagName)
	}
	if r.VariationResult != nil {
		return r.GetVariationResult(), nil
	}
	return "", fmt.Errorf("error in the configuration, no variation available for this rule")
}

// queryContext is the evaluation context as targeting queries read it. The map is built on
// first use and shared by every rule of a flag, so a flag with several targeting rules
// converts the context once per evaluation instead of once per rule. Because it is shared,
// the query engines must only read the map, never modify it. The JSON form JsonLogic reads
// is shared the same way.
type queryContext struct {
	ctx                  ffcontext.Context
	parsedEvalCtxAsMap   map[string]any
	parsedEvalCtxAsBytes []byte

	// membership is the audience store answer, nil when audiences are not configured.
	membership AudienceMembership
	// audienceStatus is the worst audience status seen during this evaluation,
	// empty when no audience term has been evaluated.
	audienceStatus string
}

func (q *queryContext) mapOnce() map[string]any {
	if q.parsedEvalCtxAsMap == nil {
		q.parsedEvalCtxAsMap = utils.ContextToMap(q.ctx)
		// the reserved attributes can only be injected by the evaluation (anti-spoofing).
		stripAudienceKeys(q.parsedEvalCtxAsMap)
	}
	return q.parsedEvalCtxAsMap
}

func (q *queryContext) jsonOnce() ([]byte, error) {
	if q.parsedEvalCtxAsBytes == nil {
		var err error
		if q.parsedEvalCtxAsBytes, err = json.Marshal(q.mapOnce()); err != nil {
			return nil, err
		}
	}
	return q.parsedEvalCtxAsBytes, nil
}

// maxEnumeratedUnknownTerms is the maximum number of unknown audience terms for which the
// outcome of the query is checked under every true/false assignment (2^n evaluations).
const maxEnumeratedUnknownTerms = 4

// queryApplies evaluates the query of the rule. Audience terms are rewritten into conditions on
// reserved attributes, whose values are injected in a copy of the shared context map (the shared
// map and its JSON form are never modified, so other rules are not impacted).
//
// A term is unknown when its membership cannot be resolved (store error, audience not configured
// i.e. nil membership as in WASM / in-process, missing or non-string attribute). When the rule has
// unknown terms, the rewritten query is evaluated under every true/false assignment of the
// unknown terms. If the outcome is the same for all of them, it does not depend on the unknown
// terms and it is used as is (onError is not consulted), e.g. `plan eq "pro" or u in audience("a")`
// for a pro user. Otherwise onError decides: skip (default) means the rule does not match and
// match means the unknown terms are considered as member. This last behavior is intentional,
// including when the membership is nil (NOT_CONFIGURED): a deny rule with onError=match must keep
// matching when the audience cannot be resolved (fail closed).
func (r *Rule) queryApplies(queryCtx *queryContext) bool {
	if r.Query == nil || !mayContainAudienceRaw(*r.Query) {
		// no audience: same path as a plain query.
		return evaluateRule(r.GetTrimmedQuery(), r.GetQueryFormat(), queryCtx)
	}
	query := r.GetTrimmedQuery()
	format := r.GetQueryFormat()
	if query == "" {
		return true
	}
	compiled, err := CompileAudienceQuery(query, format)
	if err != nil {
		slog.Error("error while compiling the audience query",
			slog.String("query", query), slog.Any("error", err))
		return false
	}
	if !compiled.HasAudience() {
		return evaluateRule(query, format, queryCtx)
	}

	base := queryCtx.mapOnce()
	injected := make(map[string]any, len(base)+len(compiled.Terms))
	for k, v := range base {
		injected[k] = v
	}
	var unknown []string
	for _, term := range compiled.Terms {
		member, known := queryCtx.resolveTerm(base, term)
		if !known {
			unknown = append(unknown, term.Key)
		}
		injected[term.Key] = member
	}
	eval := func() bool {
		return evaluateRule(compiled.Query, format, &queryContext{ctx: queryCtx.ctx, parsedEvalCtxAsMap: injected})
	}
	if len(unknown) == 0 {
		return eval()
	}

	if len(unknown) <= maxEnumeratedUnknownTerms {
		var first, differ bool
		for mask := 0; mask < 1<<len(unknown) && !differ; mask++ {
			for i, k := range unknown {
				injected[k] = mask&(1<<i) != 0
			}
			res := eval()
			if mask == 0 {
				first = res
			} else if res != first {
				differ = true
			}
		}
		if !differ {
			return first
		}
	}

	// the outcome depends on the unknown terms: onError decides.
	if r.GetOnError() != OnErrorMatch {
		return false
	}
	for _, k := range unknown {
		injected[k] = true
	}
	return eval()
}

// resolveTerm returns the membership of the term and if the answer is known.
// It also updates the audience status of the evaluation.
func (q *queryContext) resolveTerm(ctxMap map[string]any, term AudienceTerm) (member, known bool) {
	if q.membership == nil {
		q.audienceStatus = worstAudienceStatus(q.audienceStatus, AudienceStatusNotConfigured)
		return false, false
	}
	q.audienceStatus = worstAudienceStatus(q.audienceStatus, AudienceStatusHealthy)
	value, ok := AudienceAttributeValue(ctxMap, term.Attribute)
	if !ok {
		return false, false
	}
	res, found := q.membership[AudienceLookup{List: term.List, Value: value}]
	if !found || res.Err {
		q.audienceStatus = worstAudienceStatus(q.audienceStatus, AudienceStatusStoreError)
		return false, false
	}
	return res.Member, true
}

func evaluateRule(query string, queryFormat QueryFormat, queryCtx *queryContext) (success bool) {
	if query == "" {
		return true
	}
	mapCtx := queryCtx.mapOnce()

	// Catch any panics that may occur during evaluation
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(context.Background(), "panic recovered while evaluating query",
				slog.Any("panic", r),
				slog.String("query", query),
			)
			success = false
		}
	}()

	// mapCtx is shared by every rule of the flag: a query engine must not modify it.
	switch queryFormat {
	case JSONLogicQueryFormat:
		strCtx, err := queryCtx.jsonOnce()
		if err != nil {
			slog.ErrorContext(context.Background(), "error while marhsalling the context for the jsonlogic query",
				slog.Any("mapCtx", mapCtx), slog.Any("error", err.Error()))
			return false
		}
		var result bytes.Buffer
		err = jsonlogic.Apply(
			strings.NewReader(query),
			bytes.NewReader(strCtx),
			&result,
		)
		if err != nil {
			slog.ErrorContext(context.Background(), "error while evaluating the jsonlogic query",
				slog.String("query", query), slog.Any("error", err.Error()))
			return false
		}
		resStr := utils.StrTrim(result.String())
		// As per JSONLogic spec, falsy values are: false, null, "", 0, and []
		// All other values are truthy.
		switch resStr {
		case "false", "null", `""`, "0", "[]":
			return false
		default:
			return true
		}
	default:
		ev, err := getNikunjyEvaluator(query)
		if err != nil {
			slog.Error("error while parsing the nikunjy query",
				slog.String("query", query), slog.Any("error", err))
			return false
		}
		ok, err := ev.process(mapCtx)
		if err != nil {
			slog.Error("error while evaluating the nikunjy query",
				slog.String("query", query), slog.Any("error", err))
			return false
		}
		return ok
	}
}

// EvaluateProgressiveRollout is evaluating the progressive rollout for the rule.
func (r *Rule) EvaluateProgressiveRollout(
	key string,
	flagName string,
	evaluationDate time.Time,
) (string, error) {
	progressiveRolloutMaxPercentage := uint32(100 * PercentageMultiplier)
	hashID := utils.BuildHash(flagName, key, progressiveRolloutMaxPercentage)
	variation, err := r.getVariationFromProgressiveRollout(hashID, evaluationDate)
	if err != nil {
		return variation, err
	}
	return variation, nil
}

// EvaluatePercentageRollout is evaluating the percentage rollout for the rule.
func (r *Rule) EvaluatePercentageRollout(key, flagName string) (string, error) {
	m := 0.0
	for _, percentage := range r.GetPercentages() {
		m += percentage
	}
	maxPercentage := uint32(m * PercentageMultiplier)
	hashID := utils.BuildHash(flagName, key, maxPercentage)
	variationName, err := r.getVariationFromPercentage(hashID)
	if err != nil {
		return "", err
	}
	return variationName, nil
}

// IsDynamic is a function that allows to know if the rule has a dynamic result or not.
func (r *Rule) IsDynamic() bool {
	hasPercentage100 := false
	for _, percentage := range r.GetPercentages() {
		if percentage == 100 {
			hasPercentage100 = true
			break
		}
	}
	return r.ProgressiveRollout != nil ||
		(r.Percentages != nil && len(r.GetPercentages()) > 0 && !hasPercentage100)
}

func (r *Rule) getVariationFromProgressiveRollout(
	hash uint32,
	evaluationDate time.Time,
) (string, error) {
	isRolloutValid := r.ProgressiveRollout != nil &&
		r.ProgressiveRollout.Initial != nil &&
		r.ProgressiveRollout.Initial.Date != nil &&
		r.ProgressiveRollout.Initial.Variation != nil &&
		r.ProgressiveRollout.End != nil &&
		r.ProgressiveRollout.End.Date != nil &&
		r.ProgressiveRollout.End.Variation != nil &&
		r.ProgressiveRollout.End.Date.After(*r.ProgressiveRollout.Initial.Date)

	if isRolloutValid {
		if evaluationDate.Before(*r.ProgressiveRollout.Initial.Date) {
			return *r.ProgressiveRollout.Initial.Variation, nil
		}

		// We are between initial and end
		initialPercentage := r.ProgressiveRollout.Initial.getPercentage() * PercentageMultiplier
		if r.ProgressiveRollout.End.getPercentage() == 0 ||
			r.ProgressiveRollout.End.getPercentage() > 100 {
			maxPercentage := float64(100)
			r.ProgressiveRollout.End.Percentage = &maxPercentage
		}
		endPercentage := r.ProgressiveRollout.End.getPercentage() * PercentageMultiplier
		nbSec := r.ProgressiveRollout.End.Date.Unix() - r.ProgressiveRollout.Initial.Date.Unix()
		percentage := endPercentage - initialPercentage
		percentPerSec := percentage / float64(nbSec)

		var currentPercentage float64
		// Cap the percentage at endPercentage if evaluation date is at or after end date
		if !evaluationDate.Before(*r.ProgressiveRollout.End.Date) {
			currentPercentage = endPercentage
		} else {
			c := evaluationDate.Unix() - r.ProgressiveRollout.Initial.Date.Unix()
			currentPercentage = float64(c)*percentPerSec + initialPercentage
		}

		// The percentage represents how many users get the END variation
		// Users with hash < currentPercentage get END variation
		// Users with hash >= currentPercentage get INITIAL variation
		if hash < uint32(currentPercentage) {
			return r.ProgressiveRollout.End.getVariation(), nil
		}
		return r.ProgressiveRollout.Initial.getVariation(), nil
	}
	return "", fmt.Errorf("error in the progressive rollout, missing params")
}

func (r *Rule) getVariationFromPercentage(hash uint32) (string, error) {
	for key, bucket := range r.getPercentageBuckets() {
		if uint32(bucket.start) <= hash && uint32(bucket.end) > hash {
			return key, nil
		}
	}
	return "", fmt.Errorf("impossible to find the variation")
}

// getPercentageBuckets compute a map containing the buckets of each variation for this rule.
func (r *Rule) getPercentageBuckets() map[string]percentageBucket {
	percentageBuckets := make(map[string]percentageBucket, len(r.GetPercentages()))
	percentage := r.GetPercentages()

	// we need to sort the map to affect the bucket to be sure we are constantly affecting the users to the same bucket.
	// Map are not ordered in GO, so we have to order the variationNames to be able to compute the same numbers for the
	// buckets every time we are in this function.
	variationNames := make([]string, 0, len(percentage))
	for k := range percentage {
		variationNames = append(variationNames, k)
	}
	// HACK: we are reversing the sort to support the legacy format of the flags (before 1.0.0) and to be sure to always
	// have "True" before "False"
	sort.Sort(sort.Reverse(sort.StringSlice(variationNames)))

	for index, varName := range variationNames {
		startBucket := float64(0)
		if index != 0 {
			startBucket = percentageBuckets[variationNames[index-1]].end
		}
		endBucket := startBucket + (percentage[varName] * PercentageMultiplier)
		percentageBuckets[varName] = percentageBucket{
			start: startBucket,
			end:   endBucket,
		}
	}
	return percentageBuckets
}

// MergeRules is merging 2 rules.
// It is used when we have to update a rule in a scheduled rollout.
func (r *Rule) MergeRules(updatedRule Rule) {
	if updatedRule.Disable != nil {
		r.Disable = updatedRule.Disable
	}

	if updatedRule.Query != nil {
		r.Query = updatedRule.Query
	}

	if updatedRule.OnError != nil {
		r.OnError = updatedRule.OnError
	}

	if updatedRule.VariationResult != nil {
		r.VariationResult = updatedRule.VariationResult
	}

	if updatedRule.ProgressiveRollout != nil {
		c := r.GetProgressiveRollout()
		if updatedRule.ProgressiveRollout.Initial != nil {
			c.Initial.mergeStep(updatedRule.ProgressiveRollout.Initial)
		}

		if updatedRule.ProgressiveRollout.End != nil {
			c.End.mergeStep(updatedRule.ProgressiveRollout.End)
		}
		r.ProgressiveRollout = &c
	}

	if updatedRule.Percentages != nil {
		updatedPercentages := updatedRule.GetPercentages()
		mergedPercentages := r.GetPercentages()
		for key, percentage := range updatedPercentages {
			// When you set a negative percentage we are not taking it in consideration.
			if percentage < 0 {
				delete(mergedPercentages, key)
				continue
			}
			mergedPercentages[key] = percentage
		}
		r.Percentages = &mergedPercentages
	}
}

// GetTrimmedQuery is removing the break lines and return
func (r *Rule) GetTrimmedQuery() string {
	return utils.StrTrim(r.GetQuery())
}

// GetQueryFormat is returning the format used for the query
func (r *Rule) GetQueryFormat() QueryFormat {
	if utils.IsJSONObject(r.GetTrimmedQuery()) {
		return JSONLogicQueryFormat
	}
	return NikunjyQueryFormat
}

func (r *Rule) GetQuery() string {
	if r.Query == nil {
		return ""
	}

	return *r.Query
}

func (r *Rule) GetVariationResult() string {
	if r.VariationResult == nil {
		return ""
	}
	return *r.VariationResult
}

func (r *Rule) GetName() string {
	if r.Name == nil {
		return ""
	}
	return *r.Name
}

func (r *Rule) GetPercentages() map[string]float64 {
	if r.Percentages == nil {
		return map[string]float64{}
	}
	return *r.Percentages
}

func (r *Rule) IsDisable() bool {
	if r.Disable == nil {
		return false
	}
	return *r.Disable
}

func (r *Rule) GetProgressiveRollout() ProgressiveRollout {
	if r.ProgressiveRollout == nil {
		return ProgressiveRollout{
			Initial: &ProgressiveRolloutStep{},
			End:     &ProgressiveRolloutStep{},
		}
	}
	return *r.ProgressiveRollout
}

// MergeSetOfRules is taking a collection of rules and merge it with the updates
// from a schedule steps.
// If you want to edit a rule this rule should have a name already to be able to
// target the updates to the right place.
func MergeSetOfRules(initialRules, updates []Rule) *[]Rule {
	collection := initialRules
	modified := make(map[string]Rule, 0)
	for _, update := range updates {
		ruleName := update.Name
		if ruleName != nil {
			modified[update.GetName()] = update
		}
	}

	mergedUpdates := make([]string, 0)
	for index, rule := range collection {
		if _, ok := modified[rule.GetName()]; ok {
			rule.MergeRules(modified[rule.GetName()])
			collection[index] = rule
			mergedUpdates = append(mergedUpdates, rule.GetName())
		}
	}

	for _, update := range updates {
		if !utils.Contains(mergedUpdates, update.GetName()) {
			collection = append(collection, update)
		}
	}

	return &collection
}
