package linter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/cmd/cli/audiencecmd"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
)

func TestLinter_LintAudiences(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "audiences.yaml")
	require.NoError(t, os.WriteFile(confPath, []byte(`audiences:
  provider:
    kind: redis
    options:
      addrs: ["localhost:6379"]
`), 0o600))
	flagFile := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(flagFile, []byte(`f1:
  variations: {A: false, B: true}
  targeting:
    - query: targetingKey in audience("beta", "ghost")
      variation: B
  defaultRule: {variation: A}
f2:
  variations: {A: false, B: true}
  targeting:
    - query: targetingKey in audience("ghost")
      variation: B
  defaultRule: {variation: A}
f3:
  variations: {A: false}
  defaultRule: {variation: A}
`), 0o600))

	tests := []struct {
		name      string
		setup     func(*audiencetest.FakeProvider)
		audiences string
		want      []string
	}{
		{
			name:      "no --audiences: unchanged",
			audiences: "",
			want:      nil,
		},
		{
			name:      "missing lists reported per flag",
			audiences: confPath,
			setup: func(f *audiencetest.FakeProvider) {
				require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "x"))
			},
			want: []string{
				flagFile + `: flag f1: audience "ghost" does not exist`,
				flagFile + `: flag f2: audience "ghost" does not exist`,
			},
		},
		{
			name:      "all lists exist",
			audiences: confPath,
			setup: func(f *audiencetest.FakeProvider) {
				require.NoError(t, f.Add(context.Background(), "beta", audience.ListSettings{}, "x"))
				require.NoError(t, f.Add(context.Background(), "ghost", audience.ListSettings{}, "x"))
			},
			want: nil,
		},
		{
			name:      "provider unreachable",
			audiences: confPath,
			setup:     func(f *audiencetest.FakeProvider) { f.FailAll(errors.New("connection refused")) },
			want: []string{
				"impossible to check the audiences, the provider failed: connection refused",
			},
		},
		{
			name:      "audiences config missing",
			audiences: filepath.Join(t.TempDir(), "nope.yaml"),
			want:      []string{"impossible to read the audiences configuration"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			if tt.setup != nil {
				tt.setup(fake)
			}
			old := audiencecmd.ProviderFactory
			audiencecmd.ProviderFactory = func(*audienceconf.AudiencesConf) (audience.Provider, error) { return fake, nil }
			t.Cleanup(func() { audiencecmd.ProviderFactory = old })

			l := Linter{InputFile: flagFile, InputFormat: "yaml", AudiencesFile: tt.audiences}
			errs := l.Lint()
			require.Len(t, errs, len(tt.want))
			for i, w := range tt.want {
				assert.Contains(t, errs[i].Error(), w)
			}
		})
	}
}
