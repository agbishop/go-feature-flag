package audiencecmd_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/cmd/cli/audiencecmd"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

type failingInitProvider struct{ *audiencetest.FakeProvider }

func (failingInitProvider) Init(context.Context, *fflog.FFLogger) error {
	return errors.New("no store")
}

func TestLoadConfMoreErrors(t *testing.T) {
	tests := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string
	}{
		{name: "empty path", path: func(*testing.T) string { return "  " }, wantErr: "is required"},
		{
			name: "bad type",
			path: func(t *testing.T) string {
				return writeConf(t, "c.yaml", "audiences:\n  lookupTimeout: [1]\n")
			},
			wantErr: "invalid audiences configuration",
		},
		{
			name: "unreadable yaml",
			path: func(t *testing.T) string {
				return writeConf(t, "c.yaml", "audiences: [unclosed\n")
			},
			wantErr: "impossible to read the audiences configuration",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := audiencecmd.LoadConf(tt.path(t))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestOpenErrors(t *testing.T) {
	tests := []struct {
		name    string
		factory func(*audienceconf.AudiencesConf) (audience.Provider, error)
		wantErr string
	}{
		{
			name: "factory fails",
			factory: func(*audienceconf.AudiencesConf) (audience.Provider, error) {
				return nil, errors.New("bad provider")
			},
			wantErr: "bad provider",
		},
		{
			name: "init fails",
			factory: func(*audienceconf.AudiencesConf) (audience.Provider, error) {
				return failingInitProvider{audiencetest.NewFakeProvider()}, nil
			},
			wantErr: "audience provider is unreachable",
		},
	}
	conf := writeConf(t, "c.yaml", baseConf)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := audiencecmd.ProviderFactory
			audiencecmd.ProviderFactory = tt.factory
			t.Cleanup(func() { audiencecmd.ProviderFactory = old })
			s, err := audiencecmd.Open(context.Background(), conf)
			assert.ErrorContains(t, err, tt.wantErr)
			assert.Nil(t, s)
		})
	}
	t.Run("missing configuration", func(t *testing.T) {
		_, err := audiencecmd.Open(context.Background(), "")
		assert.Error(t, err)
	})
	t.Run("close is nil safe", func(t *testing.T) {
		var s *audiencecmd.Session
		assert.NotPanics(t, func() { s.Close(context.Background()) })
	})
}

func TestCommandsWithoutValidConfiguration(t *testing.T) {
	bad := writeConf(t, "bad.yaml", "foo: bar\n")
	tests := []struct {
		name string
		args []string
	}{
		{name: "exists", args: []string{"exists", "--audiences", bad, "beta"}},
		{name: "contains", args: []string{"contains", "--audiences", bad, "emails", "a"}},
		{name: "key", args: []string{"key", "--audiences", bad, "emails", "a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := run(t, tt.args...)
			assert.ErrorContains(t, err, "no `audiences` block found")
		})
	}
}

func TestExistsOutputs(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	fake := audiencetest.NewFakeProvider()
	require.NoError(t, fake.Add(context.Background(), "present", audience.ListSettings{}, "x"))
	useFake(t, fake)

	out, err := run(t, "exists", "--audiences", conf, "present", "absent")
	assert.EqualError(t, err, "1 audience list(s) do not exist")
	assert.Contains(t, out, "present: exists")
	assert.Contains(t, out, "absent: does not exist")
}

func TestContainsProviderFailure(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	fake := audiencetest.NewFakeProvider()
	fake.FailAll(errors.New("down"))
	useFake(t, fake)
	_, err := run(t, "contains", "--audiences", conf, "emails", "a")
	assert.ErrorContains(t, err, "impossible to query the audience")
}
