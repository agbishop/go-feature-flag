// Package audiencecmd holds what the CLI commands share to work with audiences: loading the
// `audiences:` configuration file and building the provider (injectable for tests), plus the
// read-only `audience` command group.
package audiencecmd

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/cmdhelpers/audienceconf"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

// ProviderFactory builds the (not initialized) provider of an audiences configuration.
// Tests replace it to inject a fake provider.
var ProviderFactory = audienceconf.InitProvider

// LoadConf reads the `audiences:` block of a relay proxy style configuration file (YAML or
// JSON, chosen by the extension) and validates it. Durations accept the same notations as
// in the relay proxy configuration (for example "50ms").
func LoadConf(path string) (*audienceconf.AudiencesConf, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("the audiences configuration file is required")
	}
	var parser koanf.Parser = yaml.Parser()
	if strings.EqualFold(filepath.Ext(path), ".json") {
		parser = json.Parser()
	}
	k := koanf.New(".")
	if err := k.Load(file.Provider(path), parser); err != nil {
		return nil, fmt.Errorf("impossible to read the audiences configuration %q: %w", path, err)
	}
	if !k.Exists("audiences") {
		return nil, fmt.Errorf("%s: no `audiences` block found", path)
	}
	conf := &audienceconf.AudiencesConf{}
	if err := k.Unmarshal("audiences", conf); err != nil {
		return nil, fmt.Errorf("%s: invalid audiences configuration: %w", path, err)
	}
	if err := conf.IsValid(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return conf, nil
}

// Session is an initialized provider with its resolver. Call Close when done.
type Session struct {
	Conf     *audienceconf.AudiencesConf
	Provider audience.Provider
	Resolver *audience.Resolver
}

// Open loads the configuration, builds and initializes the provider.
func Open(ctx context.Context, path string) (*Session, error) {
	conf, err := LoadConf(path)
	if err != nil {
		return nil, err
	}
	p, err := ProviderFactory(conf)
	if err != nil {
		return nil, err
	}
	logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.DiscardHandler)}
	if err := p.Init(ctx, logger); err != nil {
		// a failed Init can leave resources behind (e.g. a client that keeps reconnecting).
		_ = p.Shutdown(ctx)
		return nil, fmt.Errorf("audience provider is unreachable: %w", err)
	}
	return &Session{
		Conf:     conf,
		Provider: p,
		Resolver: audience.NewResolver(p, conf.EffectiveConfig(), nil, logger),
	}, nil
}

// Close shuts the provider down.
func (s *Session) Close(ctx context.Context) {
	if s == nil {
		return
	}
	s.Resolver.Close()
	if s.Provider != nil {
		_ = s.Provider.Shutdown(ctx)
	}
}
