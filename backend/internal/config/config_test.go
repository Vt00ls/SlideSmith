package config

import "testing"

func TestNormalizeRunnerProfile(t *testing.T) {
	tests := map[string]string{
		"full":              "full-ppt-master",
		" FULL-PPT-MASTER ": "full-ppt-master",
		"real-lite":         "real-lite",
		"smoke":             "smoke",
	}
	for input, want := range tests {
		got, err := NormalizeRunnerProfile(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeRunnerProfile(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeRunnerProfile("arbitrary-runner"); err == nil {
		t.Fatal("invalid runner profile was accepted")
	}
}

func TestLoadUsesFullRequestWithClosedRolloutGateByDefault(t *testing.T) {
	t.Setenv("SLIDESMITH_PPT_RUNNER_PROFILE", "")
	t.Setenv("SLIDESMITH_FULL_PPT_DEFAULT_ENABLED", "")
	t.Setenv("SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT", "")
	cfg := Load().AgentCompose
	if cfg.RunnerProfile != "full-ppt-master" || cfg.RunnerProfileExplicit {
		t.Fatalf("runner profile default = %q explicit=%v", cfg.RunnerProfile, cfg.RunnerProfileExplicit)
	}
	if cfg.FullPPTDefaultEnabled {
		t.Fatal("full rollout gate must default closed")
	}
	if !cfg.FullPPTPreflightStrict {
		t.Fatal("full preflight must default strict")
	}
}

func TestLoadTracksExplicitRunnerAndFlags(t *testing.T) {
	t.Setenv("SLIDESMITH_PPT_RUNNER_PROFILE", "real-lite")
	t.Setenv("SLIDESMITH_FULL_PPT_DEFAULT_ENABLED", "true")
	t.Setenv("SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT", "false")
	cfg := Load().AgentCompose
	if cfg.RunnerProfile != "real-lite" || !cfg.RunnerProfileExplicit || !cfg.FullPPTDefaultEnabled || cfg.FullPPTPreflightStrict {
		t.Fatalf("explicit config not preserved: %#v", cfg)
	}
}
