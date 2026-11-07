package flag

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAudienceAttributeKey(t *testing.T) {
	assert.Equal(t, "goff_aud_beta_orqxez3foruw4z2lmv4q", AudienceAttributeKey("beta", "targetingKey"))
	assert.Equal(t, "goff_aud_a_ovzwk4ronfsa", AudienceAttributeKey("a", "user.id"))
	assert.Equal(t, "goff_aud_a_mvwwc2lm", AudienceAttributeKey("a", "email"))
}

func TestCompileAudienceQuery_Nikunjy(t *testing.T) {
	tk := func(l string) string { return AudienceAttributeKey(l, "targetingKey") }
	tests := []struct {
		name      string
		query     string
		want      string
		wantTerms []AudienceTerm
	}{
		{name: "no audience", query: `country eq "US"`, want: `country eq "US"`},
		{name: "attribute named audience", query: `audience eq "x"`, want: `audience eq "x"`},
		{name: "empty query", query: ``, want: ``},
		{
			name:      "single name",
			query:     `targetingKey in audience("beta")`,
			want:      `(` + tk("beta") + ` eq true)`,
			wantTerms: []AudienceTerm{{List: "beta", Attribute: "targetingKey", Key: tk("beta")}},
		},
		{
			name:  "multiple names",
			query: `targetingKey in audience("a", "b")`,
			want:  `(` + tk("a") + ` eq true or ` + tk("b") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
				{List: "b", Attribute: "targetingKey", Key: tk("b")},
			},
		},
		{
			name:  "duplicate names in a term",
			query: `targetingKey in audience("a","a")`,
			want:  `(` + tk("a") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
			},
		},
		{
			name:  "not and other conditions",
			query: `targetingKey in audience("beta_testers") and not (targetingKey in audience("churned")) and country eq "US"`,
			want: `(` + tk("beta_testers") + ` eq true) and not ((` + tk("churned") +
				` eq true)) and country eq "US"`,
			wantTerms: []AudienceTerm{
				{List: "beta_testers", Attribute: "targetingKey", Key: tk("beta_testers")},
				{List: "churned", Attribute: "targetingKey", Key: tk("churned")},
			},
		},
		{
			name:  "dotted attribute and email",
			query: `user.id in audience("a") or email in audience("a")`,
			want: `(` + AudienceAttributeKey("a", "user.id") + ` eq true) or (` +
				AudienceAttributeKey("a", "email") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "user.id", Key: AudienceAttributeKey("a", "user.id")},
				{List: "a", Attribute: "email", Key: AudienceAttributeKey("a", "email")},
			},
		},
		{
			name:  "same term twice is distinct once",
			query: `targetingKey in audience("a") or targetingKey in audience("a")`,
			want:  `(` + tk("a") + ` eq true) or (` + tk("a") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
			},
		},
		{
			name:      "uppercase keywords",
			query:     `targetingKey IN AUDIENCE("a")`,
			want:      `(` + tk("a") + ` eq true)`,
			wantTerms: []AudienceTerm{{List: "a", Attribute: "targetingKey", Key: tk("a")}},
		},
		{
			name:      "whitespace variants",
			query:     "targetingKey  in\n audience (\t\"a\" ,\n\"b\" )",
			want:      `(` + tk("a") + ` eq true or ` + tk("b") + ` eq true)`,
			wantTerms: []AudienceTerm{{List: "a", Attribute: "targetingKey", Key: tk("a")}, {List: "b", Attribute: "targetingKey", Key: tk("b")}},
		},
		{
			name:  "audience text inside a string literal",
			query: `name eq "x in audience(\"y\")"`,
			want:  `name eq "x in audience(\"y\")"`,
		},
		{
			name:  "string literal with escaped quote before a real term",
			query: `name eq "a\"b" and targetingKey in audience("a")`,
			want:  `name eq "a\"b" and (` + tk("a") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
			},
		},
		{
			name:  "string literal with escaped backslash",
			query: `name eq "a\\" and targetingKey in audience("a")`,
			want:  `name eq "a\\" and (` + tk("a") + ` eq true)`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompileAudienceQuery(tt.query, NikunjyQueryFormat)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Query)
			assert.Equal(t, tt.wantTerms, got.Terms)
			assert.Equal(t, tt.wantTerms != nil, got.HasAudience())
		})
	}
}

func TestCompileAudienceQuery_Errors(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		format  QueryFormat
		wantErr string
	}{
		{"variable name", `targetingKey in audience(someVar)`, NikunjyQueryFormat, "string literals"},
		{"empty audience", `targetingKey in audience()`, NikunjyQueryFormat, "at least one"},
		{"invalid name uppercase", `targetingKey in audience("Beta")`, NikunjyQueryFormat, "invalid audience name"},
		{"invalid name empty", `targetingKey in audience("")`, NikunjyQueryFormat, "invalid audience name"},
		{"name too long", `targetingKey in audience("` + string(make([]byte, 0)) + `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")`, NikunjyQueryFormat, "invalid audience name"},
		{"missing paren", `targetingKey in audience "a"`, NikunjyQueryFormat, "malformed"},
		{"no parens at all", `targetingKey in audience`, NikunjyQueryFormat, "malformed"},
		{"missing closing paren", `targetingKey in audience("a"`, NikunjyQueryFormat, "malformed"},
		{"unterminated quote", `targetingKey in audience("a)`, NikunjyQueryFormat, "malformed"},
		{"missing attribute", `audience("a")`, NikunjyQueryFormat, "malformed"},
		{"not an in", `targetingKey eq audience("a")`, NikunjyQueryFormat, "malformed"},
		{"bad separator", `targetingKey in audience("a" "b")`, NikunjyQueryFormat, "malformed"},
		{"spoofing nikunjy", `goff_aud_a_xx eq true`, NikunjyQueryFormat, "reserved prefix"},
		{"spoofing jsonlogic", `{"==":[{"var":"goff_aud_a_xx"},true]}`, JSONLogicQueryFormat, "reserved prefix"},
		{"json first arg not var", `{"inAudience":["x","a"]}`, JSONLogicQueryFormat, "first argument"},
		{"json var not string", `{"inAudience":[{"var":1},"a"]}`, JSONLogicQueryFormat, "string attribute"},
		{"json no names", `{"inAudience":[{"var":"targetingKey"}]}`, JSONLogicQueryFormat, "at least one"},
		{"json not an array", `{"inAudience":"a"}`, JSONLogicQueryFormat, "expects"},
		{"json name not string", `{"inAudience":[{"var":"targetingKey"},{"var":"x"}]}`, JSONLogicQueryFormat, "string literals"},
		{"json invalid name", `{"inAudience":[{"var":"targetingKey"},"A b"]}`, JSONLogicQueryFormat, "invalid audience name"},
		{"json extra keys", `{"inAudience":[{"var":"targetingKey"},"a"],"x":1}`, JSONLogicQueryFormat, "only key"},
		{"json invalid", `{"inAudience":`, JSONLogicQueryFormat, "invalid JSONLogic"},
		{"nikunjy not in audience", `x not in audience("a")`, NikunjyQueryFormat, "not (<attribute> in audience"},
		{"nikunjy NOT IN audience", `x NOT IN audience("a")`, NikunjyQueryFormat, "not (<attribute> in audience"},
		{"nikunjy mixed case Audience", `x in Audience("a")`, NikunjyQueryFormat, "invalid audience keyword"},
		{"nikunjy mixed case aUdIeNcE", `x in aUdIeNcE ("a")`, NikunjyQueryFormat, "invalid audience keyword"},
		{"json in some", `{"some":[{"var":"items"},{"inAudience":[{"var":""},"a"]}]}`, JSONLogicQueryFormat, "iterator"},
		{"json in all", `{"all":[{"var":"items"},{"inAudience":[{"var":""},"a"]}]}`, JSONLogicQueryFormat, "iterator"},
		{"json in none", `{"none":[{"var":"items"},{"inAudience":[{"var":""},"a"]}]}`, JSONLogicQueryFormat, "iterator"},
		{"json in map", `{"map":[{"var":"items"},{"inAudience":[{"var":""},"a"]}]}`, JSONLogicQueryFormat, "iterator"},
		{"json in filter", `{"filter":[{"var":"items"},{"inAudience":[{"var":""},"a"]}]}`, JSONLogicQueryFormat, "iterator"},
		{"json in reduce nested", `{"reduce":[{"var":"items"},{"and":[{"inAudience":[{"var":""},"a"]}]},0]}`, JSONLogicQueryFormat, "iterator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CompileAudienceQuery(tt.query, tt.format)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			// errors are cached too
			_, err2 := CompileAudienceQuery(tt.query, tt.format)
			assert.EqualError(t, err2, err.Error())
		})
	}
}

func TestCompileAudienceQuery_JSONLogic(t *testing.T) {
	tk := func(l string) string { return AudienceAttributeKey(l, "targetingKey") }
	tests := []struct {
		name      string
		query     string
		want      string
		wantTerms []AudienceTerm
	}{
		{name: "no audience", query: `{"==":[{"var":"a"},1]}`, want: `{"==":[{"var":"a"},1]}`},
		{
			name:      "single",
			query:     `{"inAudience": [{"var": "targetingKey"}, "beta"]}`,
			want:      `{"==":[{"var":"` + tk("beta") + `"},true]}`,
			wantTerms: []AudienceTerm{{List: "beta", Attribute: "targetingKey", Key: tk("beta")}},
		},
		{
			name:  "multiple",
			query: `{"inAudience": [{"var": "targetingKey"}, "a", "b"]}`,
			want:  `{"or":[{"==":[{"var":"` + tk("a") + `"},true]},{"==":[{"var":"` + tk("b") + `"},true]}]}`,
			wantTerms: []AudienceTerm{
				{List: "a", Attribute: "targetingKey", Key: tk("a")},
				{List: "b", Attribute: "targetingKey", Key: tk("b")},
			},
		},
		{
			name: "nested in and/or, numbers preserved",
			query: `{"and":[{"or":[{"inAudience":[{"var":"user.id"},"a"]},{"==":[{"var":"n"},1.50]}]},` +
				`{"!":{"inAudience":[{"var":"user.id"},"a"]}}]}`,
			want: `{"and":[{"or":[{"==":[{"var":"` + AudienceAttributeKey("a", "user.id") + `"},true]},` +
				`{"==":[{"var":"n"},1.50]}]},` +
				`{"!":{"==":[{"var":"` + AudienceAttributeKey("a", "user.id") + `"},true]}}]}`,
			wantTerms: []AudienceTerm{{List: "a", Attribute: "user.id", Key: AudienceAttributeKey("a", "user.id")}},
		},
		{
			name:      "string literal mentioning inAudience is untouched",
			query:     `{"==":[{"var":"a"},"inAudience"]}`,
			want:      `{"==":[{"var":"a"},"inAudience"]}`,
			wantTerms: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompileAudienceQuery(tt.query, JSONLogicQueryFormat)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, got.Query)
			assert.Equal(t, tt.wantTerms, got.Terms)
			again, err := CompileAudienceQuery(tt.query, JSONLogicQueryFormat)
			require.NoError(t, err)
			assert.Equal(t, got, again, "deterministic and cached")
		})
	}
}

func TestCompileAudienceQuery_Cache(t *testing.T) {
	q := `targetingKey in audience("cache_test")`
	a, err := CompileAudienceQuery(q, NikunjyQueryFormat)
	require.NoError(t, err)
	_, ok := audienceQueryCache.Load(audienceCacheKey{format: NikunjyQueryFormat, query: q})
	assert.True(t, ok)
	b, err := CompileAudienceQuery(q, NikunjyQueryFormat)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestCompileAudienceQuery_Evaluation(t *testing.T) {
	member := AudienceAttributeKey
	tests := []struct {
		name   string
		query  string
		format QueryFormat
		ctx    map[string]any
		want   bool
	}{
		{
			name:   "nikunjy member",
			query:  `targetingKey in audience("beta")`,
			format: NikunjyQueryFormat,
			ctx:    map[string]any{"targetingKey": "u1", member("beta", "targetingKey"): true},
			want:   true,
		},
		{
			name:   "nikunjy not member",
			query:  `targetingKey in audience("beta")`,
			format: NikunjyQueryFormat,
			ctx:    map[string]any{"targetingKey": "u1", member("beta", "targetingKey"): false},
			want:   false,
		},
		{
			name:   "nikunjy absent key",
			query:  `targetingKey in audience("beta")`,
			format: NikunjyQueryFormat,
			ctx:    map[string]any{"targetingKey": "u1"},
			want:   false,
		},
		{
			name:   "nikunjy second name matches",
			query:  `targetingKey in audience("a", "b")`,
			format: NikunjyQueryFormat,
			ctx:    map[string]any{member("a", "targetingKey"): false, member("b", "targetingKey"): true},
			want:   true,
		},
		{
			name:   "nikunjy not (...) excluded",
			query:  `targetingKey in audience("beta") and not (targetingKey in audience("churned")) and country eq "US"`,
			format: NikunjyQueryFormat,
			ctx: map[string]any{
				"country": "US", member("beta", "targetingKey"): true, member("churned", "targetingKey"): true,
			},
			want: false,
		},
		{
			name:   "nikunjy not (...) included",
			query:  `targetingKey in audience("beta") and not (targetingKey in audience("churned")) and country eq "US"`,
			format: NikunjyQueryFormat,
			ctx: map[string]any{
				"country": "US", member("beta", "targetingKey"): true, member("churned", "targetingKey"): false,
			},
			want: true,
		},
		{
			name:   "nikunjy dotted attribute",
			query:  `user.id in audience("vip")`,
			format: NikunjyQueryFormat,
			ctx:    map[string]any{"user": map[string]any{"id": "1"}, member("vip", "user.id"): true},
			want:   true,
		},
		{
			name:   "jsonlogic member",
			query:  `{"inAudience": [{"var": "targetingKey"}, "beta", "other"]}`,
			format: JSONLogicQueryFormat,
			ctx:    map[string]any{member("other", "targetingKey"): true},
			want:   true,
		},
		{
			name:   "jsonlogic not member",
			query:  `{"inAudience": [{"var": "targetingKey"}, "beta", "other"]}`,
			format: JSONLogicQueryFormat,
			ctx:    map[string]any{member("beta", "targetingKey"): false},
			want:   false,
		},
		{
			name:   "jsonlogic negated in and",
			query:  `{"and":[{"inAudience":[{"var":"targetingKey"},"a"]},{"!":{"inAudience":[{"var":"targetingKey"},"b"]}}]}`,
			format: JSONLogicQueryFormat,
			ctx:    map[string]any{member("a", "targetingKey"): true, member("b", "targetingKey"): false},
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cq, err := CompileAudienceQuery(tt.query, tt.format)
			require.NoError(t, err)
			require.True(t, cq.HasAudience())
			got := evaluateRule(cq.Query, tt.format, &queryContext{parsedEvalCtxAsMap: tt.ctx})
			assert.Equal(t, tt.want, got)
		})
	}
}
