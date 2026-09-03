package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveKiroCacheSourceModeDefaultsToEmulationOnly(t *testing.T) {
	cases := []struct {
		name  string
		group *Group
		want  string
	}{
		{
			name:  "nil group",
			group: nil,
			want:  KiroCacheSourceModeEmulationOnly,
		},
		{
			name:  "empty value",
			group: &Group{Platform: PlatformKiro},
			want:  KiroCacheSourceModeEmulationOnly,
		},
		{
			name:  "unknown value",
			group: &Group{Platform: PlatformKiro, KiroCacheSourceMode: "bogus"},
			want:  KiroCacheSourceModeEmulationOnly,
		},
		{
			name:  "upstream_first honoured",
			group: &Group{Platform: PlatformKiro, KiroCacheSourceMode: KiroCacheSourceModeUpstreamFirst},
			want:  KiroCacheSourceModeUpstreamFirst,
		},
		{
			name:  "non-kiro platform forced to emulation_only",
			group: &Group{Platform: PlatformAnthropic, KiroCacheSourceMode: KiroCacheSourceModeUpstreamFirst},
			want:  KiroCacheSourceModeEmulationOnly,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.group.EffectiveKiroCacheSourceMode())
		})
	}
}

func TestNormalizeGroupRuntimeFieldsNormalizesCacheSourceMode(t *testing.T) {
	g := &Group{Platform: PlatformKiro, KiroCacheSourceMode: "nope"}
	NormalizeGroupRuntimeFields(g)
	require.Equal(t, KiroCacheSourceModeEmulationOnly, g.KiroCacheSourceMode)

	g = &Group{Platform: PlatformKiro, KiroCacheSourceMode: KiroCacheSourceModeUpstreamFirst}
	NormalizeGroupRuntimeFields(g)
	require.Equal(t, KiroCacheSourceModeUpstreamFirst, g.KiroCacheSourceMode)

	// 非 kiro 平台一律清回 emulation_only，避免遗留值在平台切换后生效。
	g = &Group{Platform: PlatformAnthropic, KiroCacheSourceMode: KiroCacheSourceModeUpstreamFirst}
	NormalizeGroupRuntimeFields(g)
	require.Equal(t, KiroCacheSourceModeEmulationOnly, g.KiroCacheSourceMode)
}
