package audiencetest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thomaspoignant/go-feature-flag/audience"
	"github.com/thomaspoignant/go-feature-flag/audience/audiencetest"
)

func TestFakeProviderContract(t *testing.T) {
	audiencetest.RunProviderContract(t, func(_ *testing.T) (audience.Provider, audiencetest.Seeder) {
		f := audiencetest.NewFakeProvider()
		return f, f
	})
}

func TestFakeProviderFaultInjection(t *testing.T) {
	ctx := context.Background()
	lookups := []audience.Lookup{{List: "a", Member: "x"}, {List: "b", Member: "y"}}

	newFake := func() *audiencetest.FakeProvider {
		f := audiencetest.NewFakeProvider()
		require.NoError(t, f.Add(ctx, "a", audience.ListSettings{}, "x"))
		return f
	}
	tests := []struct {
		name    string
		setup   func(f *audiencetest.FakeProvider)
		wantErr bool
		want    []audience.Result
	}{
		{
			name: "no fault",
			want: []audience.Result{{Member: true}, {Member: false}},
		},
		{
			name:    "FailAll fails the whole call",
			setup:   func(f *audiencetest.FakeProvider) { f.FailAll(assert.AnError) },
			wantErr: true,
		},
		{
			name:  "FailList fails one list only",
			setup: func(f *audiencetest.FakeProvider) { f.FailList("b", assert.AnError) },
			want:  []audience.Result{{Member: true}, {Err: assert.AnError}},
		},
		{
			name: "FailList(nil) restores the list",
			setup: func(f *audiencetest.FakeProvider) {
				f.FailList("b", assert.AnError)
				f.FailList("b", nil)
			},
			want: []audience.Result{{Member: true}, {Member: false}},
		},
		{
			name:  "a delay does not change the results",
			setup: func(f *audiencetest.FakeProvider) { f.SetDelay(time.Millisecond) },
			want:  []audience.Result{{Member: true}, {Member: false}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake()
			if tt.setup != nil {
				tt.setup(f)
			}
			got, err := f.Contains(ctx, lookups)
			if tt.wantErr {
				assert.ErrorIs(t, err, assert.AnError)
				_, err = f.Exists(ctx, map[string]audience.ListSettings{"a": {}})
				assert.ErrorIs(t, err, assert.AnError)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
			assert.Equal(t, 1, f.Calls())
			assert.Equal(t, 2, f.LookupCount())
			assert.Equal(t, lookups, f.LastLookups())
		})
	}

	t.Run("a delay is interrupted by the context", func(t *testing.T) {
		f := newFake()
		f.SetDelay(time.Minute)
		cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		_, err := f.Contains(cctx, lookups)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
