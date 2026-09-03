package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	kiropkg "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
)

func TestKiroCacheUsageDecisionMirrorsMergeBranches(t *testing.T) {
	cases := []struct {
		name         string
		sourceMode   string
		hasSimulated bool
		upstreamSeen bool
		want         string
	}{
		{
			name:         "no emulation regardless of upstream",
			sourceMode:   KiroCacheSourceModeUpstreamFirst,
			hasSimulated: false,
			upstreamSeen: true,
			want:         "no_emulation",
		},
		{
			name:         "upstream_first with upstream present",
			sourceMode:   KiroCacheSourceModeUpstreamFirst,
			hasSimulated: true,
			upstreamSeen: true,
			want:         "upstream",
		},
		{
			name:         "upstream_first falls back when upstream absent",
			sourceMode:   KiroCacheSourceModeUpstreamFirst,
			hasSimulated: true,
			upstreamSeen: false,
			want:         "emulation",
		},
		{
			name:         "emulation_only ignores upstream",
			sourceMode:   KiroCacheSourceModeEmulationOnly,
			hasSimulated: true,
			upstreamSeen: true,
			want:         "emulation",
		},
		{
			name:         "empty mode behaves as emulation_only",
			sourceMode:   "",
			hasSimulated: true,
			upstreamSeen: true,
			want:         "emulation",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, kiroCacheUsageDecision(tt.sourceMode, tt.hasSimulated, tt.upstreamSeen))
		})
	}
}

func TestLogKiroCacheUsageToleratesNilInputs(t *testing.T) {
	// 日志不应成为请求路径上的崩溃源：account 为 nil 直接返回，
	// group / simulated 为 nil 也要能安全打印。
	require.NotPanics(t, func() {
		logKiroCacheUsage(nil, nil, "m", KiroCacheSourceModeEmulationOnly, nil, kiropkg.Usage{})
	})
	require.NotPanics(t, func() {
		logKiroCacheUsage(&Account{ID: 1}, nil, "m", KiroCacheSourceModeUpstreamFirst, nil, kiropkg.Usage{})
	})
	require.NotPanics(t, func() {
		logKiroCacheUsage(
			&Account{ID: 1},
			&Group{ID: 2, Platform: PlatformKiro},
			"claude-opus-5",
			KiroCacheSourceModeUpstreamFirst,
			&kiroCacheEmulationUsage{InputTokens: 1, CacheReadInputTokens: 2},
			kiropkg.Usage{Upstream: kiropkg.UpstreamTokenUsage{Seen: true, RawJSON: `{"a":1}`}},
		)
	})
}
