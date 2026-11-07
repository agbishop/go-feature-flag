package flag

import (
	"regexp"
	"strings"
	"testing"
)

// FuzzCompileAudienceQuery checks that the audience compiler never panics, and that a
// successful compilation leaves no audience term in the rewritten query.
func FuzzCompileAudienceQuery(f *testing.F) {
	for _, query := range []string{
		`targetingKey in audience("beta")`,
		`targetingKey in audience("a", "b") and not (user.id in audience("c"))`,
		`name eq "x in audience(\"y\")"`,
		`targetingKey in audience(`,
		`audience("a")`,
		`targetingKey in audience("a\")`,
		`{"inAudience":[{"var":"targetingKey"},"a","b"]}`,
		`{"and":[{"inAudience":[{"var":"x"},"a"]},{"==":[1,1]}]}`,
		`{"inAudience":[1]}`,
		`goff_aud_a_b eq true`,
	} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		t.Cleanup(func() { audienceQueryCache.Clear() })
		for _, format := range []QueryFormat{NikunjyQueryFormat, JSONLogicQueryFormat} {
			cq, err := CompileAudienceQuery(query, format)
			if err != nil {
				continue
			}
			if !cq.HasAudience() {
				continue
			}
			if format == NikunjyQueryFormat && audienceKeywordRe.MatchString(stripNikunjyStrings(cq.Query)) {
				t.Fatalf("audience term left in rewritten query %q (from %q)", cq.Query, query)
			}
			if format == JSONLogicQueryFormat && strings.Contains(cq.Query, `"inAudience":`) {
				t.Fatalf("inAudience left in rewritten query %q (from %q)", cq.Query, query)
			}
		}
	})
}

func stripNikunjyStrings(q string) string {
	var sb strings.Builder
	for i := 0; i < len(q); {
		if q[i] == '"' {
			i = skipNikunjyString(q, i)
			continue
		}
		sb.WriteByte(q[i])
		i++
	}
	return sb.String()
}

var audienceKeywordRe = regexp.MustCompile(`(^|[^A-Za-z0-9_:.-])(audience|AUDIENCE)\s*\(`)
