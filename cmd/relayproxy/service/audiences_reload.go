package service

import (
	"reflect"

	ffclient "github.com/thomaspoignant/go-feature-flag"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/cmd/relayproxy/config"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"go.uber.org/zap"
)

// withoutAudiences returns a copy of the common flagset configuration without its audiences.
func withoutAudiences(c config.CommonFlagSet) config.CommonFlagSet {
	c.Audiences = nil
	return c
}

// audiencesChanged tells if the audiences differ.
func audiencesChanged(current, next *audienceconf.AudiencesConf) bool {
	return !reflect.DeepEqual(current, next)
}

// applyAudiences applies the new audiences configuration to the running client, without a
// restart. It returns true when the new configuration is now the one in use (applied or
// identical). Enabling or disabling the audiences of a running client is not supported.
// Nothing secret (store address, credentials) nor list name is logged.
func applyAudiences(
	client *ffclient.GoFeatureFlag, flagsetName string, metrics audience.Metrics,
	current, next *audienceconf.AudiencesConf, logger *zap.Logger,
) bool {
	if !audiencesChanged(current, next) {
		return true
	}
	if client == nil || current == nil || next == nil {
		logger.Error("enabling or disabling the audiences is not supported at runtime: the change is "+
			"ignored and will only be applied the next time the relay proxy is restarted.",
			zap.String("flagset", flagsetName))
		return false
	}
	update := &ffclient.AudiencesConfig{Config: next.EffectiveConfig(), Metrics: metrics}
	sameProvider := current.SameProvider(next)
	if !sameProvider {
		provider, err := audienceconf.InitProvider(next)
		if err != nil {
			logger.Error("impossible to apply the new audiences configuration, the previous one is kept",
				zap.String("flagset", flagsetName), zap.Error(err))
			return false
		}
		update.Provider = provider
	}
	if err := client.UpdateAudiences(update); err != nil {
		logger.Error("impossible to apply the new audiences configuration, the previous one is kept",
			zap.String("flagset", flagsetName), zap.Error(err))
		return false
	}
	logger.Info("Configuration changed: audiences updated without restart",
		zap.String("flagset", flagsetName),
		zap.Bool("providerReplaced", !sameProvider),
		zap.Int("definitions", len(next.Definitions)),
		zap.Int("cacheSize", next.Cache.Size))
	return true
}
