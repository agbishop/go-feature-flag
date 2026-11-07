package flag

import "maps"

// constructMetadata is the internal generic func used to enhance model.VariationResult adding
// the targeting.rule's name (from configuration) to the Metadata.
// That way, it is possible to see when a targeting rule is match during the evaluation process.
func constructMetadata(
	flagMetadata map[string]any, ruleName *string, audienceStatus string,
) map[string]any {
	metadata := maps.Clone(flagMetadata)
	hasRuleName := ruleName != nil && *ruleName != ""
	if !hasRuleName && audienceStatus == "" {
		return metadata
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if hasRuleName {
		metadata["evaluatedRuleName"] = *ruleName
	}
	if audienceStatus != "" {
		metadata[AudienceStatusMetadataKey] = audienceStatus
	}
	return metadata
}
