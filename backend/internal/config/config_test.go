package config

import (
	"testing"
	"time"
)

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

func TestLoadResourcePolicyDefaultsOfflineAndFailClosed(t *testing.T) {
	for _, key := range []string{
		"SLIDESMITH_RESOURCE_PHASE_ENABLED", "SLIDESMITH_RESOURCE_NETWORK_ENABLED",
		"SLIDESMITH_RESOURCE_WEB_IMAGE_ENABLED", "SLIDESMITH_RESOURCE_AI_IMAGE_ENABLED",
		"SLIDESMITH_RESOURCE_FORMULA_NETWORK_ENABLED", "SLIDESMITH_RESOURCE_MAX_FILES",
		"SLIDESMITH_RESOURCE_MAX_TOTAL_BYTES", "SLIDESMITH_RESOURCE_MAX_SINGLE_BYTES",
		"SLIDESMITH_RESOURCE_TIMEOUT", "SLIDESMITH_RESOURCE_ALLOWED_AI_PROVIDERS",
	} {
		t.Setenv(key, "")
	}
	cfg := Load().AgentCompose
	if cfg.ResourcePhaseEnabled || cfg.ResourceNetworkEnabled || cfg.ResourceWebEnabled || cfg.ResourceAIEnabled || cfg.ResourceFormulaNetwork {
		t.Fatalf("resource defaults are not offline/fail-closed: %#v", cfg)
	}
	if cfg.ResourceAIPaths != "api" || cfg.ResourceAIProviders != "" || cfg.ResourceWebProviders != "openverse,wikimedia" {
		t.Fatalf("resource provider defaults = %#v", cfg)
	}
	if cfg.ResourceMaxFiles != 100 || cfg.ResourceMaxTotalBytes != 524288000 || cfg.ResourceMaxSingleBytes != 52428800 || cfg.ResourceTimeout != 20*time.Minute {
		t.Fatalf("resource limit defaults = %#v", cfg)
	}
}

func TestLoadResourcePolicyOverrides(t *testing.T) {
	t.Setenv("SLIDESMITH_RESOURCE_PHASE_ENABLED", "true")
	t.Setenv("SLIDESMITH_RESOURCE_NETWORK_ENABLED", "true")
	t.Setenv("SLIDESMITH_RESOURCE_WEB_IMAGE_ENABLED", "true")
	t.Setenv("SLIDESMITH_RESOURCE_AI_IMAGE_ENABLED", "true")
	t.Setenv("SLIDESMITH_RESOURCE_FORMULA_NETWORK_ENABLED", "true")
	t.Setenv("SLIDESMITH_RESOURCE_AI_PATHS", "api,host-native")
	t.Setenv("SLIDESMITH_RESOURCE_ALLOWED_WEB_PROVIDERS", "wikimedia")
	t.Setenv("SLIDESMITH_RESOURCE_ALLOWED_AI_PROVIDERS", "openai")
	t.Setenv("SLIDESMITH_RESOURCE_MAX_FILES", "20")
	t.Setenv("SLIDESMITH_RESOURCE_MAX_TOTAL_BYTES", "4096")
	t.Setenv("SLIDESMITH_RESOURCE_MAX_SINGLE_BYTES", "1024")
	t.Setenv("SLIDESMITH_RESOURCE_TIMEOUT", "90s")
	cfg := Load().AgentCompose
	if !cfg.ResourcePhaseEnabled || !cfg.ResourceNetworkEnabled || !cfg.ResourceWebEnabled || !cfg.ResourceAIEnabled || !cfg.ResourceFormulaNetwork {
		t.Fatalf("resource overrides missing: %#v", cfg)
	}
	if cfg.ResourceAIPaths != "api,host-native" || cfg.ResourceWebProviders != "wikimedia" || cfg.ResourceAIProviders != "openai" {
		t.Fatalf("resource provider overrides = %#v", cfg)
	}
	if cfg.ResourceMaxFiles != 20 || cfg.ResourceMaxTotalBytes != 4096 || cfg.ResourceMaxSingleBytes != 1024 || cfg.ResourceTimeout != 90*time.Second {
		t.Fatalf("resource limit overrides = %#v", cfg)
	}
}
