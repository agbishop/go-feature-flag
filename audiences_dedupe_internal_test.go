package ffclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
	"github.com/thomaspoignant/go-feature-flag/modules/core/flag"
	"github.com/thomaspoignant/go-feature-flag/modules/core/testutils/testconvert"
	"github.com/thomaspoignant/go-feature-flag/utils/fflog"
)

type dedupeBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (b *dedupeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.Write(p)
}

func (b *dedupeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sb.String()
}

func audienceFlagFor(t *testing.T, list string) flag.Flag {
	t.Helper()
	var f flag.InternalFlag
	require.NoError(t, json.Unmarshal([]byte(`{
  "variations": {"A": false, "B": true},
  "targeting": [{"name": "r", "query": "targetingKey in audience(\"`+list+`\")", "variation": "B"}],
  "defaultRule": {"variation": "A"}
}`), &f))
	return &f
}

func TestAudiencesRuntime_ExistenceWarningIsNotDuplicated(t *testing.T) {
	tests := []struct {
		name  string
		calls []map[string]string // flag key -> list, one map per check
		want  map[string]int      // list -> expected number of warnings
	}{
		{
			name:  "the same flags checked twice warn once (initial check + first reload)",
			calls: []map[string]string{{"f1": "vip"}, {"f1": "vip"}},
			want:  map[string]int{"vip": 1},
		},
		{
			name:  "a flag that now uses another list is checked",
			calls: []map[string]string{{"f1": "vip"}, {"f1": "ghost"}},
			want:  map[string]int{"vip": 1, "ghost": 1},
		},
		{
			name:  "a new flag with a new list is checked, the old one is not",
			calls: []map[string]string{{"f1": "vip"}, {"f1": "vip", "f2": "ghost"}},
			want:  map[string]int{"vip": 1, "ghost": 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &dedupeBuf{}
			logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.NewTextHandler(buf, nil))}
			rt := newAudiencesRuntime(Config{
				Audiences:      &AudiencesConfig{Provider: audiencetest.NewFakeProvider()},
				internalLogger: logger,
			})
			defer rt.close(context.Background())
			for _, call := range tt.calls {
				flags := map[string]flag.Flag{}
				for k, l := range call {
					flags[k] = audienceFlagFor(t, l)
				}
				rt.checkExistence(flags)
				// wait for the asynchronous check to be over
				assert.Eventually(t, func() bool {
					rt.mu.Lock()
					defer rt.mu.Unlock()
					return len(rt.pending) == 0
				}, 2*time.Second, 5*time.Millisecond)
			}
			for list, n := range tt.want {
				assert.Equal(t, n, strings.Count(buf.String(), "audience="+list), list)
			}
		})
	}

	t.Run("concurrent checks of the same list warn once", func(t *testing.T) {
		buf := &dedupeBuf{}
		logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.NewTextHandler(buf, nil))}
		rt := newAudiencesRuntime(Config{
			Audiences:      &AudiencesConfig{Provider: audiencetest.NewFakeProvider()},
			internalLogger: logger,
		})
		flags := map[string]flag.Flag{"f1": audienceFlagFor(t, "vip")}
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); rt.checkExistence(flags) }()
		}
		wg.Wait()
		rt.close(context.Background())
		assert.Equal(t, 1, strings.Count(buf.String(), "audience=vip"))
	})

	t.Run("a changed flag checks its missing list again", func(t *testing.T) {
		buf := &dedupeBuf{}
		logger := &fflog.FFLogger{LeveledLogger: slog.New(slog.NewTextHandler(buf, nil))}
		rt := newAudiencesRuntime(Config{
			Audiences:      &AudiencesConfig{Provider: audiencetest.NewFakeProvider()},
			internalLogger: logger,
		})
		defer rt.close(context.Background())
		wait := func() {
			assert.Eventually(t, func() bool {
				rt.mu.Lock()
				defer rt.mu.Unlock()
				return len(rt.pending) == 0
			}, 2*time.Second, 5*time.Millisecond)
		}
		first := audienceFlagFor(t, "vip").(*flag.InternalFlag)
		rt.checkExistence(map[string]flag.Flag{"f1": first})
		wait()
		changed := audienceFlagFor(t, "vip").(*flag.InternalFlag)
		changed.Disable = testconvert.Bool(true)
		rt.checkExistence(map[string]flag.Flag{"f1": changed})
		wait()
		assert.Equal(t, 2, strings.Count(buf.String(), "audience=vip"))
	})
}
