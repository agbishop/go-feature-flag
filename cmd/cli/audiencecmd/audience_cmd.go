package audiencecmd

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/redisprovider"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
)

// NewAudienceCmd is the read-only `audience` command group. It never writes to the store.
func NewAudienceCmd() *cobra.Command {
	var confPath string
	cmd := &cobra.Command{
		Use:   "audience",
		Short: "👥 Inspect the audiences (read-only)",
		Long: "👥 Inspect the external audience lists used by the audience() query term. " +
			"These commands only read the store, they never write to it.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&confPath, "audiences", "",
		"Path to a YAML or JSON file with an `audiences:` block (relay proxy format)")
	_ = cmd.MarkPersistentFlagRequired("audiences")

	cmd.AddCommand(&cobra.Command{
		Use:           "exists <list>...",
		Short:         "Tell if the audience lists exist in the store",
		Args:          cobra.MinimumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExists(cmd, confPath, args)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "contains <list> <id>...",
		Short: "Tell if the ids are members of an audience list",
		Long: "Tell if the ids are members of an audience list. The ids are normalized, hashed and " +
			"encoded with the settings of the list, exactly as GO Feature Flag does.",
		Args:          cobra.MinimumNArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContains(cmd, confPath, args[0], args[1:])
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "key <list> <id>",
		Short: "Print the store key and member value GO Feature Flag looks up (offline)",
		Long: "Print the exact key and member value GO Feature Flag looks up for an id. " +
			"It does not connect to the store.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKey(cmd, confPath, args[0], args[1])
		},
	})
	return cmd
}

func runExists(cmd *cobra.Command, confPath string, lists []string) error {
	ctx := context.Background()
	s, err := Open(ctx, confPath)
	if err != nil {
		return err
	}
	defer s.Close(ctx)
	res, err := s.Resolver.CheckExists(ctx, lists)
	if err != nil {
		return fmt.Errorf("impossible to check the audiences: %w", err)
	}
	missing := 0
	for _, l := range lists {
		if found, known := res[l]; !known {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(),
				"%s: unknown (not found within the bounded scan of the store, it may not exist)\n", l)
		} else if found {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: exists\n", l)
		} else {
			missing++
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: does not exist\n", l)
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d audience list(s) do not exist", missing)
	}
	return nil
}

func runContains(cmd *cobra.Command, confPath, list string, ids []string) error {
	ctx := context.Background()
	s, err := Open(ctx, confPath)
	if err != nil {
		return err
	}
	defer s.Close(ctx)
	settings := settingsOf(s.Conf, list)
	lookups := make([]audience.Lookup, len(ids))
	for i, id := range ids {
		lookups[i] = audience.Lookup{List: list, Member: audience.EncodeMember(id, settings), Settings: settings}
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout(s))
	defer cancel()
	results, err := s.Provider.Contains(ctx, lookups)
	if err != nil {
		return fmt.Errorf("impossible to query the audience %q: %w", list, err)
	}
	if len(results) != len(ids) {
		return fmt.Errorf("the provider returned %d results for %d ids", len(results), len(ids))
	}
	failed := 0
	for i, id := range ids {
		switch {
		case results[i].Err != nil:
			failed++
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: error (%v)\n", id, results[i].Err)
		case results[i].Member:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: member\n", id)
		default:
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: not member\n", id)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d lookup(s) failed", failed)
	}
	return nil
}

func lookupTimeout(s *Session) (d time.Duration) {
	// the CLI is interactive: use the configured timeout, or a generous default.
	if d = s.Conf.LookupTimeout; d > 0 {
		return d
	}
	return 5 * time.Second
}

func runKey(cmd *cobra.Command, confPath, list, id string) error {
	conf, err := LoadConf(confPath)
	if err != nil {
		return err
	}
	st := settingsOf(conf, list)
	key, member := redisprovider.StoreKey(list, id, st)
	out := cmd.OutOrStdout()
	_, _ = fmt.Fprintf(out, "layout:  %s\n", st.Layout)
	if st.Layout == audience.LayoutKeys {
		_, _ = fmt.Fprintf(out, "key:     %s\n", keyDisplay(key, st, member))
		return nil
	}
	_, _ = fmt.Fprintf(out, "key:     %s\n", key)
	if st.Encoding == audience.EncodingRaw && st.Hash == audience.HashSHA256 {
		_, _ = fmt.Fprintf(out, "member:  %s (hex of the 32 raw bytes)\n", hex.EncodeToString([]byte(member)))
		_, _ = fmt.Fprintln(out, "note:    encoding is raw, the member is stored as binary, not as text")
		return nil
	}
	_, _ = fmt.Fprintf(out, "member:  %s\n", member)
	return nil
}

// keyDisplay renders a keys layout key; with the raw encoding the id part is binary so it is
// printed as hex.
func keyDisplay(key string, st audience.ListSettings, member string) string {
	if st.Encoding == audience.EncodingRaw && st.Hash == audience.HashSHA256 {
		return key[:len(key)-len(member)] + "<hex:" + hex.EncodeToString([]byte(member)) +
			"> (id part is the 32 raw bytes)"
	}
	return key
}

func settingsOf(conf *audienceconf.AudiencesConf, list string) audience.ListSettings {
	cfg := conf.EffectiveConfig()
	return cfg.Resolve(list)
}
