package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/retriever/fileretriever"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func redisAudiences(addr string) *audienceconf.AudiencesConf {
	return &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind:    audienceconf.RedisProvider,
		Options: &audienceconf.RedisOptions{Addrs: []string{addr}, DialTimeoutMs: 200},
	}}
}

func TestApplyAudiences_NotApplied(t *testing.T) {
	file := filepath.Join(t.TempDir(), "flags.yaml")
	require.NoError(t, os.WriteFile(file, []byte("f:\n  variations: {a: 1, b: 2}\n  defaultRule: {variation: a}\n"), 0o600))
	client, err := ffclient.New(ffclient.Config{
		PollingInterval: time.Hour,
		Retriever:       &fileretriever.Retriever{Path: file},
		Audiences:       &ffclient.AudiencesConfig{Provider: audiencetest.NewFakeProvider()},
	})
	require.NoError(t, err)
	defer client.Close()

	invalidKind := redisAudiences("127.0.0.1:1")
	invalidKind.Provider.Kind = "nope"
	badFailMode := redisAudiences("127.0.0.1:1")
	badFailMode.FailMode = "nope"

	tests := []struct {
		name    string
		client  *ffclient.GoFeatureFlag
		current *audienceconf.AudiencesConf
		next    *audienceconf.AudiencesConf
		wantLog string
	}{
		{
			name: "enabling at runtime is not supported", client: client,
			current: nil, next: redisAudiences("127.0.0.1:1"), wantLog: "not supported at runtime",
		},
		{
			name: "disabling at runtime is not supported", client: client,
			current: redisAudiences("127.0.0.1:1"), next: nil, wantLog: "not supported at runtime",
		},
		{
			name: "no client", client: nil,
			current: redisAudiences("127.0.0.1:1"), next: redisAudiences("127.0.0.1:2"), wantLog: "not supported at runtime",
		},
		{
			name: "new provider cannot be built", client: client,
			current: redisAudiences("127.0.0.1:1"), next: invalidKind, wantLog: "the previous one is kept",
		},
		{
			name: "invalid new configuration", client: client,
			current: redisAudiences("127.0.0.1:1"), next: badFailMode, wantLog: "the previous one is kept",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.InfoLevel)
			got := applyAudiences(tt.client, "default", audience.NopMetrics{}, tt.current, tt.next, zap.New(core))
			assert.False(t, got)
			require.Equal(t, 1, logs.Len())
			assert.Contains(t, logs.All()[0].Message, tt.wantLog)
		})
	}

	t.Run("identical configuration is a no-op", func(t *testing.T) {
		core, logs := observer.New(zap.InfoLevel)
		c := redisAudiences("127.0.0.1:1")
		assert.True(t, applyAudiences(client, "default", audience.NopMetrics{}, c, redisAudiences("127.0.0.1:1"), zap.New(core)))
		assert.Zero(t, logs.Len())
	})
}
