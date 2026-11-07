package flag

import (
	"bytes"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// AudienceAttributePrefix is the prefix of the reserved context attributes injected
// by the evaluation to tell if the evaluation context belongs to an audience.
//
// The prefix has to start with a letter: the nikunjy grammar does not accept an
// attribute name starting with an underscore (the leading characters are silently
// dropped by the lexer). It is reserved: a query containing it is rejected.
const AudienceAttributePrefix = "goff_aud_"

const (
	nikunjyAudienceKeyword  = "audience"
	jsonLogicAudienceOp     = "inAudience"
	audienceNameMaxLength   = 64
	nikunjyAudienceKeywordU = "AUDIENCE"
)

var (
	audienceNameRegexp = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	// same charset as the nikunjy ATTRNAME rule, with '.' as the sub attribute separator.
	audienceAttrRegexp = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_:-]*(\.[A-Za-z][A-Za-z0-9_:-]*)*$`)
	audienceKeyEncoder = base32.StdEncoding.WithPadding(base32.NoPadding)
)

// AudienceTerm is an audience reference found in a query.
type AudienceTerm struct {
	// List is the audience name.
	List string
	// Attribute is the context attribute path, e.g. "targetingKey" or "user.id".
	Attribute string
	// Key is the reserved attribute key injected into the query context.
	Key string
}

// CompiledQuery is the result of the compilation of a query containing audience terms.
type CompiledQuery struct {
	// Query is the rewritten query (equal to the input when there is no audience term).
	Query string
	// Terms are the distinct (List, Attribute) pairs, in first-appearance order.
	// It is nil when the query has no audience term.
	Terms []AudienceTerm
}

// HasAudience returns true if the query contains at least one audience term.
func (c CompiledQuery) HasAudience() bool { return len(c.Terms) > 0 }

// AudienceAttributeKey returns the reserved context attribute key for the pair (list, attribute).
func AudienceAttributeKey(list, attribute string) string {
	return AudienceAttributePrefix + list + "_" +
		strings.ToLower(audienceKeyEncoder.EncodeToString([]byte(attribute)))
}

type audienceCacheKey struct {
	format QueryFormat
	query  string
}

type audienceCacheValue struct {
	compiled CompiledQuery
	err      error
}

// audienceQueryCache memoizes the compilation per (format, trimmed query).
// Like nikunjyEvaluatorCache, entries are never evicted: queries are part of the static flag config.
var audienceQueryCache sync.Map // map[audienceCacheKey]audienceCacheValue

// CompileAudienceQuery rewrites the audience terms of a (trimmed) query into conditions on
// reserved context attributes. The result (and the error) is cached and safe for concurrent use.
func CompileAudienceQuery(query string, format QueryFormat) (CompiledQuery, error) {
	if !mayContainAudience(query, format) {
		return CompiledQuery{Query: query}, nil
	}
	if format != JSONLogicQueryFormat {
		format = NikunjyQueryFormat
	}
	ck := audienceCacheKey{format: format, query: query}
	if v, ok := audienceQueryCache.Load(ck); ok {
		cv := v.(audienceCacheValue)
		return cv.compiled, cv.err
	}
	var cv audienceCacheValue
	if format == JSONLogicQueryFormat {
		cv.compiled, cv.err = compileJSONLogicAudience(query)
	} else {
		cv.compiled, cv.err = compileNikunjyAudience(query)
	}
	actual, _ := audienceQueryCache.LoadOrStore(ck, cv)
	acv := actual.(audienceCacheValue)
	return acv.compiled, acv.err
}

func mayContainAudience(query string, format QueryFormat) bool {
	if strings.Contains(query, AudienceAttributePrefix) {
		return true
	}
	if format == JSONLogicQueryFormat {
		return strings.Contains(query, jsonLogicAudienceOp)
	}
	return containsFoldASCII(query, nikunjyAudienceKeyword)
}

var errAudienceSpoof = fmt.Errorf("the query must not contain the reserved prefix %q", AudienceAttributePrefix)

func validateAudienceName(name string) error {
	if !audienceNameRegexp.MatchString(name) {
		return fmt.Errorf("invalid audience name %q: must match ^[a-z0-9_]{1,%d}$", name, audienceNameMaxLength)
	}
	return nil
}

// termCollector keeps the distinct terms in first-appearance order.
type termCollector struct {
	terms []AudienceTerm
	seen  map[[2]string]struct{}
}

func (tc *termCollector) add(list, attr string) string {
	key := AudienceAttributeKey(list, attr)
	id := [2]string{list, attr}
	if tc.seen == nil {
		tc.seen = map[[2]string]struct{}{}
	}
	if _, ok := tc.seen[id]; !ok {
		tc.seen[id] = struct{}{}
		tc.terms = append(tc.terms, AudienceTerm{List: list, Attribute: attr, Key: key})
	}
	return key
}

// ---- nikunjy ----

type nikToken struct {
	word       bool
	start, end int
}

func isNikunjyWordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == ':' || c == '-' || c == '.'
}

func isAudienceSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// skipNikunjyString returns the index following the string literal starting at i (s[i] == '"').
// A backslash escapes the next character. An unterminated literal ends at len(s).
func skipNikunjyString(s string, i int) int {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return len(s)
}

func skipAudienceSpaces(s string, i int) int {
	for i < len(s) && isAudienceSpace(s[i]) {
		i++
	}
	return i
}

// checkAudienceKeywordCase rejects a mixed-case audience keyword used as a call, e.g. Audience("a").
func checkAudienceKeywordCase(query, word string, wordEnd int) error {
	if word == nikunjyAudienceKeyword || word == nikunjyAudienceKeywordU ||
		!strings.EqualFold(word, nikunjyAudienceKeyword) {
		return nil
	}
	if n := skipAudienceSpaces(query, wordEnd); n < len(query) && query[n] == '(' {
		return fmt.Errorf("invalid audience keyword %q: use audience or AUDIENCE", word)
	}
	return nil
}

func compileNikunjyAudience(query string) (CompiledQuery, error) {
	if strings.Contains(query, AudienceAttributePrefix) {
		return CompiledQuery{}, errAudienceSpoof
	}
	var (
		out  strings.Builder
		tc   termCollector
		prev []nikToken // last significant tokens (at most 2)
		last int        // start of the not yet copied text
	)
	push := func(t nikToken) {
		prev = append(prev, t)
		if len(prev) > 2 {
			prev = prev[1:]
		}
	}
	i := 0
	for i < len(query) {
		c := query[i]
		switch {
		case c == '"':
			j := skipNikunjyString(query, i)
			push(nikToken{start: i, end: j})
			i = j
		case isNikunjyWordChar(c):
			j := i
			for j < len(query) && isNikunjyWordChar(query[j]) {
				j++
			}
			word := query[i:j]
			if err := checkAudienceKeywordCase(query, word, j); err != nil {
				return CompiledQuery{}, err
			}
			if word == nikunjyAudienceKeyword || word == nikunjyAudienceKeywordU {
				repl, start, end, matched, err := rewriteNikunjyTerm(query, j, prev, &tc)
				if err != nil {
					return CompiledQuery{}, err
				}
				if matched {
					out.WriteString(query[last:start])
					out.WriteString(repl)
					last = end
					prev = nil
					i = end
					continue
				}
			}
			push(nikToken{word: true, start: i, end: j})
			i = j
		case isAudienceSpace(c):
			i++
		default:
			push(nikToken{start: i, end: i + 1})
			i++
		}
	}
	if tc.terms == nil {
		return CompiledQuery{Query: query}, nil
	}
	out.WriteString(query[last:])
	return CompiledQuery{Query: out.String(), Terms: tc.terms}, nil
}

// rewriteNikunjyTerm handles an audience keyword whose last character is at wordEnd-1.
// prev holds the two tokens preceding the keyword. When the keyword is part of an
// audience term, it returns the replacement text and the replaced range [start, end).
// A keyword that is neither preceded by `<attr> in` nor followed by '(' is a plain attribute name.
func rewriteNikunjyTerm(query string, wordEnd int, prev []nikToken, tc *termCollector) (
	repl string, start, end int, matched bool, err error,
) {
	afterIn := len(prev) == 2 && prev[1].word && prev[0].word &&
		(query[prev[1].start:prev[1].end] == "in" || query[prev[1].start:prev[1].end] == "IN")
	next := skipAudienceSpaces(query, wordEnd)
	if !afterIn {
		if next < len(query) && query[next] == '(' {
			return "", 0, 0, false, errors.New(
				"malformed audience term: expected `<attribute> in audience(\"name\", ...)`")
		}
		return "", 0, 0, false, nil
	}
	attr := query[prev[0].start:prev[0].end]
	if strings.EqualFold(attr, "not") {
		return "", 0, 0, false, errors.New(
			"`<attribute> not in audience(...)` is not supported: use `not (<attribute> in audience(...))`")
	}
	if !audienceAttrRegexp.MatchString(attr) {
		return "", 0, 0, false, fmt.Errorf("invalid attribute %q in audience term", attr)
	}
	names, end, err := parseNikunjyAudienceNames(query, wordEnd)
	if err != nil {
		return "", 0, 0, false, err
	}
	conds := make([]string, 0, len(names))
	for _, n := range names {
		conds = append(conds, tc.add(n, attr)+" eq true")
	}
	return "(" + strings.Join(conds, " or ") + ")", prev[0].start, end, true, nil
}

// parseNikunjyAudienceNames parses `("a", "b")` starting at i, just after the audience keyword.
// It returns the distinct names and the index following the closing parenthesis.
func parseNikunjyAudienceNames(s string, i int) ([]string, int, error) {
	i = skipAudienceSpaces(s, i)
	if i >= len(s) || s[i] != '(' {
		return nil, 0, errors.New("malformed audience term: missing '(' after audience")
	}
	i = skipAudienceSpaces(s, i+1)
	if i < len(s) && s[i] == ')' {
		return nil, 0, errors.New("audience() requires at least one audience name")
	}
	var names []string
	for {
		i = skipAudienceSpaces(s, i)
		if i >= len(s) {
			return nil, 0, errors.New("malformed audience term: missing closing ')'")
		}
		if s[i] != '"' {
			return nil, 0, errors.New("malformed audience term: audience names must be string literals")
		}
		j := skipNikunjyString(s, i)
		if s[j-1] != '"' || j-i < 2 {
			return nil, 0, errors.New("malformed audience term: unterminated string literal")
		}
		name := s[i+1 : j-1]
		if err := validateAudienceName(name); err != nil {
			return nil, 0, err
		}
		dup := false
		for _, n := range names {
			if n == name {
				dup = true
				break
			}
		}
		if !dup {
			names = append(names, name)
		}
		i = skipAudienceSpaces(s, j)
		if i >= len(s) {
			return nil, 0, errors.New("malformed audience term: missing closing ')'")
		}
		switch s[i] {
		case ',':
			i++
		case ')':
			return names, i + 1, nil
		default:
			return nil, 0, fmt.Errorf("malformed audience term: unexpected character %q", s[i])
		}
	}
}

// ---- JSONLogic ----

func compileJSONLogicAudience(query string) (CompiledQuery, error) {
	if strings.Contains(query, AudienceAttributePrefix) {
		return CompiledQuery{}, errAudienceSpoof
	}
	dec := json.NewDecoder(strings.NewReader(query))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return CompiledQuery{}, fmt.Errorf("invalid JSONLogic query: %w", err)
	}
	var tc termCollector
	rewritten, err := rewriteJSONLogicAudience(root, &tc, false)
	if err != nil {
		return CompiledQuery{}, err
	}
	if tc.terms == nil {
		return CompiledQuery{Query: query}, nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(rewritten); err != nil {
		return CompiledQuery{}, fmt.Errorf("impossible to encode the rewritten JSONLogic query: %w", err)
	}
	return CompiledQuery{Query: strings.TrimSpace(buf.String()), Terms: tc.terms}, nil
}

// jsonLogicIteratorOps are the operators whose logic argument is evaluated against each element
// of an array: a var inside resolves against the element, not the evaluation context.
var jsonLogicIteratorOps = map[string]struct{}{
	"some": {}, "all": {}, "none": {}, "map": {}, "filter": {}, "reduce": {},
}

func rewriteJSONLogicAudience(node any, tc *termCollector, inIterator bool) (any, error) {
	switch v := node.(type) {
	case []any:
		for i := range v {
			r, err := rewriteJSONLogicAudience(v[i], tc, inIterator)
			if err != nil {
				return nil, err
			}
			v[i] = r
		}
		return v, nil
	case map[string]any:
		if args, ok := v[jsonLogicAudienceOp]; ok {
			if inIterator {
				return nil, fmt.Errorf(
					"%s can't be used inside an iterator operator (some, all, none, map, filter, reduce): "+
						"the var would be resolved against the array element", jsonLogicAudienceOp)
			}
			if len(v) != 1 {
				return nil, fmt.Errorf("%s must be the only key of its JSONLogic object", jsonLogicAudienceOp)
			}
			return rewriteInAudience(args, tc)
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic term order
		for _, k := range keys {
			var r any
			var err error
			if _, iter := jsonLogicIteratorOps[k]; iter {
				r, err = rewriteIteratorArgs(v[k], tc, inIterator)
			} else {
				r, err = rewriteJSONLogicAudience(v[k], tc, inIterator)
			}
			if err != nil {
				return nil, err
			}
			v[k] = r
		}
		return v, nil
	default:
		return node, nil
	}
}

// rewriteIteratorArgs rewrites the arguments of an iterator operator: the first one (the array)
// is evaluated against the context, the others against each element.
func rewriteIteratorArgs(args any, tc *termCollector, inIterator bool) (any, error) {
	list, ok := args.([]any)
	if !ok {
		return rewriteJSONLogicAudience(args, tc, true)
	}
	for i := range list {
		r, err := rewriteJSONLogicAudience(list[i], tc, inIterator || i > 0)
		if err != nil {
			return nil, err
		}
		list[i] = r
	}
	return list, nil
}

func rewriteInAudience(args any, tc *termCollector) (any, error) {
	list, ok := args.([]any)
	if !ok || len(list) < 2 {
		return nil, fmt.Errorf(
			`%s expects [{"var": "<attribute>"}, "<audience>", ...] with at least one audience name`, jsonLogicAudienceOp)
	}
	varObj, ok := list[0].(map[string]any)
	if !ok || len(varObj) != 1 {
		return nil, fmt.Errorf(`the first argument of %s must be {"var": "<attribute>"}`, jsonLogicAudienceOp)
	}
	attr, ok := varObj["var"].(string)
	if !ok || attr == "" {
		return nil, fmt.Errorf(`the first argument of %s must be {"var": "<attribute>"} with a string attribute`,
			jsonLogicAudienceOp)
	}
	var conds []any
	seen := map[string]struct{}{}
	for _, a := range list[1:] {
		name, ok := a.(string)
		if !ok {
			return nil, fmt.Errorf("audience names of %s must be string literals", jsonLogicAudienceOp)
		}
		if err := validateAudienceName(name); err != nil {
			return nil, err
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		key := tc.add(name, attr)
		conds = append(conds, map[string]any{"==": []any{map[string]any{"var": key}, true}})
	}
	if len(conds) == 1 {
		return conds[0], nil
	}
	return map[string]any{"or": conds}, nil
}
