package audiencecmd_test

import (
	"bytes"
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

const (
	aliceSHA = "ff8d9819fc0e12bf0d24892e45987e249a28dce836a85cad60e28eaaa8c6d976" // sha256("alice@example.com")
	baseConf = `audiences:
  provider:
    kind: redis
    prefix: "goff:"
    options:
      addrs: ["localhost:6379"]
  lookupTimeout: 1s
  definitions:
    emails:
      normalize: trimLower
      hash: sha256
    emails-keys:
      layout: keys
      normalize: trimLower
      hash: sha256
    emails-raw:
      normalize: trimLower
      hash: sha256
      encoding: raw
`
)

func writeConf(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func useFake(t *testing.T, fake *audiencetest.FakeProvider) {
	t.Helper()
	old := audiencecmd.ProviderFactory
	audiencecmd.ProviderFactory = func(*audienceconf.AudiencesConf) (audience.Provider, error) { return fake, nil }
	t.Cleanup(func() { audiencecmd.ProviderFactory = old })
}

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := audiencecmd.NewAudienceCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestExists(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{name: "all exist", args: []string{"beta"}, want: "beta: exists\n"},
		{
			name:    "one missing",
			args:    []string{"beta", "ghost"},
			want:    "beta: exists\nghost: does not exist\n",
			wantErr: "1 audience list(s) do not exist",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := audiencetest.NewFakeProvider()
			require.NoError(t, fake.Add(context.Background(), "beta", audience.ListSettings{}, "x"))
			useFake(t, fake)
			out, err := run(t, append([]string{"exists", "--audiences", conf}, tt.args...)...)
			assert.Equal(t, tt.want, out)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.EqualError(t, err, tt.wantErr)
			}
		})
	}
}

func TestExistsProviderFailure(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	fake := audiencetest.NewFakeProvider()
	fake.FailAll(errors.New("boom"))
	useFake(t, fake)
	_, err := run(t, "exists", "--audiences", conf, "beta")
	assert.ErrorContains(t, err, "boom")
}

func TestContains(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	fake := audiencetest.NewFakeProvider()
	require.NoError(t, fake.Add(context.Background(), "emails", audience.ListSettings{}, aliceSHA))
	useFake(t, fake)

	out, err := run(t, "contains", "--audiences", conf, "emails", "  Alice@Example.com ", "bob@example.com")
	require.NoError(t, err)
	assert.Equal(t, "  Alice@Example.com : member\nbob@example.com: not member\n", out)

	// the lookup sent to the store is encoded, never the clear id
	last := fake.LastLookups()
	require.Len(t, last, 2)
	assert.Equal(t, aliceSHA, last[0].Member)
}

func TestContainsErrors(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	fake := audiencetest.NewFakeProvider()
	fake.FailList("emails", errors.New("nope"))
	useFake(t, fake)
	out, err := run(t, "contains", "--audiences", conf, "emails", "a")
	assert.EqualError(t, err, "1 lookup(s) failed")
	assert.Contains(t, out, "a: error (nope)")
}

func TestKey(t *testing.T) {
	conf := writeConf(t, "c.yaml", baseConf)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "set layout, hashed",
			args: []string{"emails", "  Alice@Example.com"},
			want: "layout:  set\nkey:     goff:{emails}\nmember:  " + aliceSHA + "\n",
		},
		{
			name: "keys layout, hashed",
			args: []string{"emails-keys", "Alice@Example.com"},
			want: "layout:  keys\nkey:     goff:emails-keys:" + aliceSHA + "\n",
		},
		{
			name: "no hash, no normalize",
			args: []string{"other", "Alice"},
			want: "layout:  set\nkey:     goff:{other}\nmember:  Alice\n",
		},
		{
			name: "raw encoding printed as hex",
			args: []string{"emails-raw", "alice@example.com"},
			want: "layout:  set\nkey:     goff:{emails-raw}\nmember:  " + aliceSHA +
				" (hex of the 32 raw bytes)\nnote:    encoding is raw, the member is stored as binary, not as text\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// offline: a provider factory that would fail proves the store is not used
			old := audiencecmd.ProviderFactory
			audiencecmd.ProviderFactory = func(*audienceconf.AudiencesConf) (audience.Provider, error) {
				return nil, errors.New("must not be called")
			}
			t.Cleanup(func() { audiencecmd.ProviderFactory = old })
			out, err := run(t, append([]string{"key", "--audiences", conf}, tt.args...)...)
			require.NoError(t, err)
			assert.Equal(t, tt.want, out)
		})
	}
}

func TestLoadConfErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{name: "no audiences block", content: "foo: bar\n", wantErr: "no `audiences` block found"},
		{name: "invalid kind", content: "audiences:\n  provider:\n    kind: nope\n", wantErr: "invalid provider kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := audiencecmd.LoadConf(writeConf(t, "c.yaml", tt.content))
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
	_, err := audiencecmd.LoadConf(filepath.Join(t.TempDir(), "missing.yaml"))
	assert.Error(t, err)
}

func TestLoadConfJSON(t *testing.T) {
	c, err := audiencecmd.LoadConf(writeConf(t, "c.json",
		`{"audiences":{"provider":{"kind":"redis","options":{"addrs":["a:1"]}},"lookupTimeout":"2s"}}`))
	require.NoError(t, err)
	assert.Equal(t, "2s", c.LookupTimeout.String())
}

func TestRequiresAudiencesFlag(t *testing.T) {
	_, err := run(t, "exists", "beta")
	assert.Error(t, err)
}
