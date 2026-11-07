package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/retrieverconf"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWarnAudiencePrefixCollisions(t *testing.T) {
	redisRetriever := retrieverconf.RetrieverConf{Kind: retrieverconf.RedisRetriever, RedisPrefix: "goff:"}
	tests := []struct {
		name     string
		fs       *config.FlagSet
		wantLogs int
	}{
		{
			name: "same prefix warns once",
			fs: &config.FlagSet{Name: "fs", CommonFlagSet: config.CommonFlagSet{
				Retriever: &redisRetriever,
				Audiences: audiencesWithPrefix("goff:"),
			}},
			wantLogs: 1,
		},
		{
			name: "different prefix does not warn",
			fs: &config.FlagSet{CommonFlagSet: config.CommonFlagSet{
				Retriever: &redisRetriever,
				Audiences: audiencesWithPrefix("other:"),
			}},
		},
		{name: "no audiences does not warn", fs: &config.FlagSet{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			warnAudiencePrefixCollisions(tt.fs, zap.New(core))
			require.Equal(t, tt.wantLogs, logs.Len())
		})
	}
}

func audiencesWithPrefix(prefix string) *audienceconf.AudiencesConf {
	return &audienceconf.AudiencesConf{Provider: audienceconf.ProviderConf{
		Kind: audienceconf.RedisProvider, Prefix: prefix,
	}}
}
