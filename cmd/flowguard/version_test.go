package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionFromBuildInfo(t *testing.T) {
	withVersion := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/shakerShekhYousef/flowguard", Version: v}}
	}

	cases := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"go install of a tag", withVersion("v0.1.1"), true, "v0.1.1"},
		{"local build in a git checkout", withVersion("v0.1.1-0.20261027130000-abcdef123456+dirty"), true, "v0.1.1-0.20261027130000-abcdef123456+dirty"},
		{"build without module version", withVersion("(devel)"), true, "dev"},
		{"empty version", withVersion(""), true, "dev"},
		{"no build info", nil, false, "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionFromBuildInfo(tc.info, tc.ok); got != tc.want {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, tc.want)
			}
		})
	}
}
