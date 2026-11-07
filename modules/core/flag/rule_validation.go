package flag

import (
	"fmt"
	"strings"

	"github.com/diegoholiveira/jsonlogic/v3"
	"github.com/nikunjy/rules/parser"
)

// IsValid is checking if the rule is valid
func (r *Rule) IsValid(defaultRule bool, variations map[string]*any) error {
	if !defaultRule && r.IsDisable() {
		return nil
	}

	if r.Percentages == nil && r.ProgressiveRollout == nil && r.VariationResult == nil {
		return fmt.Errorf("impossible to return value")
	}

	if err := r.isQueryValid(defaultRule); err != nil {
		return err
	}

	if err := r.validatePercentages(variations); err != nil {
		return err
	}

	if err := r.validateProgressiveRollout(variations); err != nil {
		return err
	}

	if err := r.validateVariationResult(variations); err != nil {
		return err
	}

	return nil
}

// validatePercentages validates the percentage configuration of the rule.
// It checks that percentages are not empty, the sum is not zero, and all
// referenced variations exist in the provided variations map.
func (r *Rule) validatePercentages(variations map[string]*any) error {
	if r.Percentages == nil {
		return nil
	}

	percentages := r.GetPercentages()
	if len(percentages) == 0 {
		return fmt.Errorf("invalid percentages: should not be empty")
	}

	count := float64(0)
	for k, p := range percentages {
		count += p
		if _, ok := variations[k]; !ok {
			return fmt.Errorf("invalid percentage: variation %s does not exist", k)
		}
	}

	if count == 0 {
		return fmt.Errorf("invalid percentages: should not be equal to 0")
	}

	return nil
}

// validateProgressiveRollout validates the progressive rollout configuration of the rule.
// It checks that the initial percentage is lower than the end percentage, and that
// both the initial and end variations exist in the provided variations map.
func (r *Rule) validateProgressiveRollout(variations map[string]*any) error {
	if r.ProgressiveRollout == nil {
		return nil
	}

	progRollout := r.GetProgressiveRollout()
	initialPct := progRollout.Initial.getPercentage()
	endPct := progRollout.End.getPercentage()

	if endPct < initialPct {
		return fmt.Errorf(
			"invalid progressive rollout, initial percentage should be lower "+
				"than end percentage: %v/%v",
			initialPct,
			endPct,
		)
	}

	endVar := progRollout.End.getVariation()
	if _, ok := variations[endVar]; !ok {
		return fmt.Errorf(
			"invalid progressive rollout, end variation %s does not exist",
			endVar,
		)
	}

	initialVar := progRollout.Initial.getVariation()
	if _, ok := variations[initialVar]; !ok {
		return fmt.Errorf(
			"invalid progressive rollout, initial variation %s does not exist",
			initialVar,
		)
	}

	return nil
}

// validateVariationResult validates the simple variation result configuration of the rule.
// It checks that the variation result exists in the provided variations map.
// This validation only applies when the rule uses a simple variation result
// (not percentages or progressive rollout).
func (r *Rule) validateVariationResult(variations map[string]*any) error {
	if r.Percentages != nil || r.ProgressiveRollout != nil || r.VariationResult == nil {
		return nil
	}

	if _, ok := variations[r.GetVariationResult()]; !ok {
		return fmt.Errorf("invalid variation: %s does not exist", r.GetVariationResult())
	}

	return nil
}

// validateAudience does only the audience specific validation of a rule: the onError value,
// the compilation of the audience terms and no audience in the defaultRule.
// It doesn't validate the rest of the query.
func (r *Rule) validateAudience(defaultRule bool) error {
	if !defaultRule && r.IsDisable() {
		return nil
	}
	if oe := r.GetOnError(); oe != OnErrorSkip && oe != OnErrorMatch {
		return fmt.Errorf("invalid onError %q: should be %q or %q", oe, OnErrorSkip, OnErrorMatch)
	}
	if r.Query == nil || !mayContainAudienceRaw(*r.Query) {
		return nil
	}
	compiled, err := CompileAudienceQuery(r.GetTrimmedQuery(), r.GetQueryFormat())
	if err != nil {
		return err
	}
	if defaultRule && compiled.HasAudience() {
		return fmt.Errorf("audience can't be used in the defaultRule")
	}
	return nil
}

// isQueryValid validates the query configuration of the rule.
// It checks that the query is not empty and that the query format is valid.
// The query format can be either JSONLogic or Nikunjy.
func (r *Rule) isQueryValid(defaultRule bool) error {
	if oe := r.GetOnError(); oe != OnErrorSkip && oe != OnErrorMatch {
		return fmt.Errorf("invalid onError %q: should be %q or %q", oe, OnErrorSkip, OnErrorMatch)
	}

	if defaultRule {
		// audience can't be used in the default rule.
		if r.Query != nil {
			compiled, err := CompileAudienceQuery(r.GetTrimmedQuery(), r.GetQueryFormat())
			if err != nil {
				return err
			}
			if compiled.HasAudience() {
				return fmt.Errorf("audience can't be used in the defaultRule")
			}
		}
		return nil
	}

	if r.Query == nil {
		return fmt.Errorf("each targeting should have a query")
	}

	compiled, err := CompileAudienceQuery(r.GetTrimmedQuery(), r.GetQueryFormat())
	if err != nil {
		return err
	}

	// Validate the rewritten query with the parser
	switch r.GetQueryFormat() {
	case JSONLogicQueryFormat:
		if !jsonlogic.IsValid(strings.NewReader(compiled.Query)) {
			return fmt.Errorf("invalid jsonlogic query")
		}
		return nil
	default:
		return validateNikunjyQuery(compiled.Query)
	}
}

// validateNikunjyQuery validates the Nikunjy query configuration of the rule.
func validateNikunjyQuery(query string) error {
	ev, err := parser.NewEvaluator(query)
	if err != nil {
		return err
	}
	_, err = ev.Process(map[string]any{})
	if err != nil {
		return fmt.Errorf("invalid query: %w", err)
	}
	return nil
}
