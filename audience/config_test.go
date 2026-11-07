package audience_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/thomaspoignant/go-feature-flag/audience"
)

func TestConfig_IsValid(t *testing.T) {
	tests := []struct {
		name    string
		cfg     audience.Config
		wantErr string
	}{
		{name: "zero value", cfg: audience.Config{}},
		{
			name: "full config",
			cfg: audience.Config{
				Cache:         audience.CacheConfig{Size: 10, TTL: time.Second},
				LookupTimeout: time.Second,
				FailMode:      "empty",
				Defaults:      audience.ListSettings{Layout: "set"},
				Definitions: map[string]audience.ListSettings{
					"beta":  {Normalize: "trimLower", Hash: "sha256", Encoding: "raw"},
					"big_*": {Layout: "keys"},
				},
			},
		},
		{name: "negative size", cfg: audience.Config{Cache: audience.CacheConfig{Size: -1}}, wantErr: "cache.size"},
		{name: "size without ttl", cfg: audience.Config{Cache: audience.CacheConfig{Size: 1}}, wantErr: "cache.ttl"},
		{name: "negative ttl", cfg: audience.Config{Cache: audience.CacheConfig{TTL: -1}}, wantErr: "cache.ttl"},
		{name: "negative timeout", cfg: audience.Config{LookupTimeout: -1}, wantErr: "lookupTimeout"},
		{name: "sub ms timeout", cfg: audience.Config{LookupTimeout: 50}, wantErr: "duration string like 50ms"},
		{
			name:    "sub ms ttl",
			cfg:     audience.Config{Cache: audience.CacheConfig{Size: 1, TTL: 5}},
			wantErr: "duration string like 5s",
		},
		{name: "1ms timeout", cfg: audience.Config{LookupTimeout: time.Millisecond}},
		{name: "sub ms ttl without cache", cfg: audience.Config{Cache: audience.CacheConfig{TTL: 0}}},
		{name: "bad failMode", cfg: audience.Config{FailMode: "boom"}, wantErr: "failMode"},
		{
			name:    "bad default layout",
			cfg:     audience.Config{Defaults: audience.ListSettings{Layout: "tree"}},
			wantErr: "layout",
		},
		{
			name: "bad definition hash",
			cfg: audience.Config{Definitions: map[string]audience.ListSettings{
				"a": {Hash: "md5"},
			}},
			wantErr: `definitions["a"]: invalid hash`,
		},
		{
			name: "bad glob",
			cfg: audience.Config{Definitions: map[string]audience.ListSettings{
				"a[": {Layout: "set"},
			}},
			wantErr: "glob",
		},
		{
			name: "raw without hash",
			cfg: audience.Config{Definitions: map[string]audience.ListSettings{
				"a": {Encoding: "raw"},
			}},
			wantErr: "raw requires hash",
		},
		{
			name: "raw inherits hash from defaults",
			cfg: audience.Config{
				Defaults:    audience.ListSettings{Hash: "sha256"},
				Definitions: map[string]audience.ListSettings{"a": {Encoding: "raw"}},
			},
		},
		{
			name:    "raw default without hash",
			cfg:     audience.Config{Defaults: audience.ListSettings{Encoding: "raw"}},
			wantErr: "raw requires hash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.IsValid()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfig_Resolve(t *testing.T) {
	cfg := audience.Config{
		FailMode: "empty",
		Defaults: audience.ListSettings{Prefix: "d:", Normalize: "trimLower"},
		Definitions: map[string]audience.ListSettings{
			"beta":    {Prefix: "segments:", Hash: "sha256"},
			"big_*":   {Layout: "keys"},
			"big_eu*": {Layout: "set", Prefix: "eu:"},
			"b*":      {Prefix: "b:"},
			"churned": {FailMode: "unknown"},
		},
	}
	tests := []struct {
		name string
		list string
		want audience.ListSettings
	}{
		{
			name: "no definition uses defaults then built-ins",
			list: "other",
			want: audience.ListSettings{Prefix: "d:", Layout: "set", Normalize: "trimLower", Hash: "none", Encoding: "hex", FailMode: "empty"},
		},
		{
			name: "exact beats glob and inherits defaults",
			list: "beta",
			want: audience.ListSettings{Prefix: "segments:", Layout: "set", Normalize: "trimLower", Hash: "sha256", Encoding: "hex", FailMode: "empty"},
		},
		{
			name: "longest glob wins",
			list: "big_eu_1",
			want: audience.ListSettings{Prefix: "eu:", Layout: "set", Normalize: "trimLower", Hash: "none", Encoding: "hex", FailMode: "empty"},
		},
		{
			name: "glob",
			list: "big_us",
			want: audience.ListSettings{Prefix: "d:", Layout: "keys", Normalize: "trimLower", Hash: "none", Encoding: "hex", FailMode: "empty"},
		},
		{
			name: "per list failMode",
			list: "churned",
			want: audience.ListSettings{Prefix: "d:", Layout: "set", Normalize: "trimLower", Hash: "none", Encoding: "hex", FailMode: "unknown"},
		},
		{
			name: "shorter glob when longer does not match",
			list: "bxyz",
			want: audience.ListSettings{Prefix: "b:", Layout: "set", Normalize: "trimLower", Hash: "none", Encoding: "hex", FailMode: "empty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cfg.Resolve(tt.list))
		})
	}

	t.Run("equal length globs resolve lexically, deterministically", func(t *testing.T) {
		c := audience.Config{Definitions: map[string]audience.ListSettings{
			"a*": {Prefix: "A"}, "*b": {Prefix: "B"},
		}}
		for i := 0; i < 20; i++ {
			assert.Equal(t, "B", c.Resolve("ab").Prefix) // "*b" < "a*"
		}
	})
	t.Run("zero config", func(t *testing.T) {
		var c audience.Config
		assert.Equal(t, audience.ListSettings{Layout: "set", Normalize: "none", Hash: "none", Encoding: "hex", FailMode: "unknown"}, c.Resolve("x"))
	})
}

func TestEncodeMember(t *testing.T) {
	const bobSHA = "581d5f1930c81e73906dfbae2d46df90e4a1221064763a4e55ee85dab8312938"
	bobRaw, _ := hex.DecodeString(bobSHA)
	tests := []struct {
		name     string
		value    string
		settings audience.ListSettings
		want     string
	}{
		{"none", " Bob@X.com ", audience.ListSettings{Normalize: "none", Hash: "none"}, " Bob@X.com "},
		{"zero value settings", "Bob", audience.ListSettings{}, "Bob"},
		{"trimLower", " Bob@X.com ", audience.ListSettings{Normalize: "trimLower"}, "bob@x.com"},
		{"sha256 hex", "bob@x.com", audience.ListSettings{Hash: "sha256", Encoding: "hex"}, bobSHA},
		{"sha256 default encoding is hex", "bob@x.com", audience.ListSettings{Hash: "sha256"}, bobSHA},
		{"trimLower then sha256", " Bob@X.com\n", audience.ListSettings{Normalize: "trimLower", Hash: "sha256"}, bobSHA},
		{"sha256 not normalized", "Bob@x.com", audience.ListSettings{Hash: "sha256"}, func() string {
			return audience.EncodeMember("Bob@x.com", audience.ListSettings{Hash: "sha256"})
		}()},
		{"sha256 raw", "bob@x.com", audience.ListSettings{Hash: "sha256", Encoding: "raw"}, string(bobRaw)},
		{"raw without hash is ignored", "bob", audience.ListSettings{Encoding: "raw"}, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, audience.EncodeMember(tt.value, tt.settings))
		})
	}
	assert.NotEqual(t, bobSHA, audience.EncodeMember("Bob@x.com", audience.ListSettings{Hash: "sha256"}))
	assert.Len(t, audience.EncodeMember("x", audience.ListSettings{Hash: "sha256", Encoding: "raw"}), 32)
}
