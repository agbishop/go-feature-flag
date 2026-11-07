package evaluate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/cmd/cli/audiencecmd"
	"github.com/thomaspoignant/go-feature-flag/cmd/cli/evaluate"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
)

func TestCmdEvaluateAudiences(t *testing.T) {
	confPath := filepath.Join(t.TempDir(), "audiences.yaml")
	require.NoError(t, os.WriteFile(confPath, []byte(`audiences:
  provider:
    kind: redis
    options:
      addrs: ["localhost:6379"]
`), 0o600))

	tests := []struct {
		name       string
		withConf   bool
		member     bool
		wantValue  bool
		wantReason string
		wantStatus string
	}{
		{name: "without --audiences: not configured", wantValue: false, wantReason: "DEFAULT", wantStatus: "NOT_CONFIGURED"},
		{name: "member of the list", withConf: true, member: true, wantValue: true, wantReason: "TARGETING_MATCH", wantStatus: "HEALTHY"},
		{name: "not a member", withConf: true, member: false, wantValue: false, wantReason: "DEFAULT", wantStatus: "HEALTHY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "other"))
			if tt.member {
				require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "user-123"))
			}
			old := audiencecmd.ProviderFactory
			audiencecmd.ProviderFactory = func(*audienceconf.AudiencesConf) (audience.Provider, error) { return fake, nil }
			t.Cleanup(func() { audiencecmd.ProviderFactory = old })

			args := []string{
				"--kind", "file", "--path", "testdata/flag-audience.goff.yaml",
				"--flag", "aud-flag", "--ctx", `{"targetingKey": "user-123"}`, "--format", "yaml",
			}
			if tt.withConf {
				args = append(args, "--audiences", confPath)
			}
			cmd := evaluate.NewEvaluateCmd()
			out := &bytes.Buffer{}
			cmd.SetOut(out)
			cmd.SetArgs(args)
			require.NoError(t, cmd.Execute())

			var res map[string]struct {
				Value    bool           `json:"value"`
				Reason   string         `json:"reason"`
				Metadata map[string]any `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(out.Bytes(), &res), out.String())
			assert.Equal(t, tt.wantValue, res["aud-flag"].Value)
			assert.Equal(t, tt.wantReason, res["aud-flag"].Reason)
			assert.Equal(t, tt.wantStatus, res["aud-flag"].Metadata["gofeatureflag_audienceStatus"])
		})
	}
}
