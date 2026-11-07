package linter

import (
	"context"
	"fmt"
	"sort"

	"github.com/thomaspoignant/go-feature-flag/cmd/cli/audiencecmd"
	helper "github.com/thomaspoignant/go-feature-flag/cmdhelpers/configfile"
)

type Linter struct {
	InputFile   string
	InputFormat string
	// AudiencesFile (optional) is a configuration file with an `audiences:` block. When set,
	// the lists used by the flags must exist in the audience store.
	AudiencesFile string
}

func (l *Linter) Lint() []error {
	flags, err := helper.LoadConfigFile(
		l.InputFile,
		l.InputFormat,
		helper.ConfigFileDefaultLocations,
	)
	if err != nil {
		return []error{err}
	}
	errs := make([]error, 0)
	// lists maps an audience list name to the sorted keys of the valid flags using it.
	lists := map[string][]string{}
	for key, flagDto := range flags {
		flag := flagDto.Convert()
		if err := flag.IsValid(); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid flag %s: %w", l.InputFile, key, err))
			continue
		}
		seen := map[string]struct{}{}
		for _, t := range flag.AudienceTerms() {
			if _, ok := seen[t.List]; !ok {
				seen[t.List] = struct{}{}
				lists[t.List] = append(lists[t.List], key)
			}
		}
	}
	if l.AudiencesFile != "" {
		errs = append(errs, l.checkAudiences(lists)...)
	}
	return errs
}

// checkAudiences reports the audience lists used by the flags that are not in the store.
func (l *Linter) checkAudiences(lists map[string][]string) []error {
	ctx := context.Background()
	s, err := audiencecmd.Open(ctx, l.AudiencesFile)
	if err != nil {
		return []error{err}
	}
	defer s.Close(ctx)
	if len(lists) == 0 {
		return nil
	}
	names := make([]string, 0, len(lists))
	for n := range lists {
		names = append(names, n)
	}
	sort.Strings(names)
	exists, err := s.Resolver.CheckExists(ctx, names)
	if err != nil {
		return []error{fmt.Errorf("impossible to check the audiences, the provider failed: %w", err)}
	}
	var errs []error
	for _, n := range names {
		if found, known := exists[n]; found || !known {
			continue // exists, or the store could not tell within a bounded effort
		}
		keys := lists[n]
		sort.Strings(keys)
		for _, k := range keys {
			errs = append(errs, fmt.Errorf("%s: flag %s: audience %q does not exist", l.InputFile, k, n))
		}
	}
	return errs
}
