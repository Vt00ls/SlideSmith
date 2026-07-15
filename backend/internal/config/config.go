package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server       ServerConfig
	Database     DatabaseConfig
	Storage      StorageConfig
	AgentCompose AgentComposeConfig
	Worker       WorkerConfig
}

type ServerConfig struct {
	HTTPAddr        string
	GinMode         string
	ShutdownTimeout time.Duration
}

type DatabaseConfig struct {
	Driver string
	DSN    string
}

type StorageConfig struct {
	Root string
}

type AgentComposeConfig struct {
	Enabled                           bool
	CLI                               string
	Host                              string
	ComposeFile                       string
	WorkDir                           string
	WorkspaceRoot                     string
	PPTMasterSkillDir                 string
	Agent                             string
	RuntimeImage                      string
	RunnerProfile                     string
	RunnerProfileExplicit             bool
	FullPPTDefaultEnabled             bool
	FullPPTPreflightStrict            bool
	QualityGateEnabled                bool
	QualityGateStrict                 bool
	PPTXValidateEnabled               bool
	VisualReviewEnabled               bool
	VisualReviewDefault               bool
	BeautifyEnabled                   bool
	BeautifyFidelityStrict            bool
	BeautifySourceSVGReferenceEnabled bool
	ResourcePhaseEnabled              bool
	ResourceNetworkEnabled            bool
	ResourceWebEnabled                bool
	ResourceAIEnabled                 bool
	ResourceAIPaths                   string
	ResourceMaxFiles                  int
	ResourceMaxTotalBytes             int64
	ResourceMaxSingleBytes            int64
	ResourceTimeout                   time.Duration
	ResourceWebProviders              string
	ResourceAIProviders               string
	ResourceFormulaNetwork            bool
	SessionDataRoot                   string
	Timeout                           time.Duration
}

type WorkerConfig struct {
	PollInterval time.Duration
	BatchSize    int
}

func Load() Config {
	runnerProfile, runnerProfileExplicit := os.LookupEnv("SLIDESMITH_PPT_RUNNER_PROFILE")
	if strings.TrimSpace(runnerProfile) == "" {
		runnerProfile = "full-ppt-master"
		runnerProfileExplicit = false
	}
	return Config{
		Server: ServerConfig{
			HTTPAddr:        env("SLIDESMITH_HTTP_ADDR", ":8080"),
			GinMode:         env("SLIDESMITH_GIN_MODE", "debug"),
			ShutdownTimeout: envDuration("SLIDESMITH_SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		Database: DatabaseConfig{
			Driver: env("SLIDESMITH_DB_DRIVER", "sqlite"),
			DSN:    env("SLIDESMITH_DB_DSN", "data/slidesmith.db"),
		},
		Storage: StorageConfig{
			Root: env("SLIDESMITH_STORAGE_ROOT", "storage"),
		},
		AgentCompose: AgentComposeConfig{
			Enabled:                           envBool("SLIDESMITH_AGENT_COMPOSE_ENABLED", false),
			CLI:                               env("SLIDESMITH_AGENT_COMPOSE_CLI", "agent-compose"),
			Host:                              env("SLIDESMITH_AGENT_COMPOSE_HOST", ""),
			ComposeFile:                       env("SLIDESMITH_AGENT_COMPOSE_FILE", "../runtime/ppt-master-agent/agent-compose.yml"),
			WorkDir:                           env("SLIDESMITH_AGENT_COMPOSE_WORKDIR", "../runtime/ppt-master-agent"),
			WorkspaceRoot:                     env("SLIDESMITH_AGENT_COMPOSE_WORKSPACE_ROOT", ""),
			PPTMasterSkillDir:                 env("SLIDESMITH_PPT_MASTER_SKILL_DIR", ""),
			Agent:                             env("SLIDESMITH_AGENT_COMPOSE_AGENT", "ppt_master"),
			RuntimeImage:                      env("SLIDESMITH_AGENT_COMPOSE_RUNTIME_IMAGE", "slidesmith/ppt-master-runtime:dev"),
			RunnerProfile:                     runnerProfile,
			RunnerProfileExplicit:             runnerProfileExplicit,
			FullPPTDefaultEnabled:             envBool("SLIDESMITH_FULL_PPT_DEFAULT_ENABLED", false),
			FullPPTPreflightStrict:            envBool("SLIDESMITH_FULL_PPT_PREFLIGHT_STRICT", true),
			QualityGateEnabled:                envBool("SLIDESMITH_QUALITY_GATE_ENABLED", false),
			QualityGateStrict:                 envBool("SLIDESMITH_QUALITY_GATE_STRICT", true),
			PPTXValidateEnabled:               envBool("SLIDESMITH_PPTX_VALIDATE_ENABLED", false),
			VisualReviewEnabled:               envBool("SLIDESMITH_VISUAL_REVIEW_ENABLED", false),
			VisualReviewDefault:               envBool("SLIDESMITH_VISUAL_REVIEW_DEFAULT", false),
			BeautifyEnabled:                   envBool("SLIDESMITH_BEAUTIFY_ENABLED", false),
			BeautifyFidelityStrict:            envBool("SLIDESMITH_BEAUTIFY_FIDELITY_STRICT", true),
			BeautifySourceSVGReferenceEnabled: envBool("SLIDESMITH_BEAUTIFY_SOURCE_SVG_REFERENCE_ENABLED", false),
			ResourcePhaseEnabled:              envBool("SLIDESMITH_RESOURCE_PHASE_ENABLED", false),
			ResourceNetworkEnabled:            envBool("SLIDESMITH_RESOURCE_NETWORK_ENABLED", false),
			ResourceWebEnabled:                envBool("SLIDESMITH_RESOURCE_WEB_IMAGE_ENABLED", false),
			ResourceAIEnabled:                 envBool("SLIDESMITH_RESOURCE_AI_IMAGE_ENABLED", false),
			ResourceAIPaths:                   env("SLIDESMITH_RESOURCE_AI_PATHS", "api"),
			ResourceMaxFiles:                  envInt("SLIDESMITH_RESOURCE_MAX_FILES", 100),
			ResourceMaxTotalBytes:             envInt64("SLIDESMITH_RESOURCE_MAX_TOTAL_BYTES", 524288000),
			ResourceMaxSingleBytes:            envInt64("SLIDESMITH_RESOURCE_MAX_SINGLE_BYTES", 52428800),
			ResourceTimeout:                   envDuration("SLIDESMITH_RESOURCE_TIMEOUT", 20*time.Minute),
			ResourceWebProviders:              env("SLIDESMITH_RESOURCE_ALLOWED_WEB_PROVIDERS", "openverse,wikimedia"),
			ResourceAIProviders:               env("SLIDESMITH_RESOURCE_ALLOWED_AI_PROVIDERS", ""),
			ResourceFormulaNetwork:            envBool("SLIDESMITH_RESOURCE_FORMULA_NETWORK_ENABLED", false),
			SessionDataRoot:                   env("SLIDESMITH_AGENT_COMPOSE_SESSION_ROOT", ""),
			Timeout:                           envDuration("SLIDESMITH_AGENT_COMPOSE_TIMEOUT", 30*time.Minute),
		},
		Worker: WorkerConfig{
			PollInterval: envDuration("SLIDESMITH_WORKER_POLL_INTERVAL", 2*time.Second),
			BatchSize:    envInt("SLIDESMITH_WORKER_BATCH_SIZE", 1),
		},
	}
}

func NormalizeRunnerProfile(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "full", "full-ppt-master":
		return "full-ppt-master", nil
	case "real-lite":
		return "real-lite", nil
	case "smoke":
		return "smoke", nil
	case "native-template-fill":
		return "native-template-fill", nil
	default:
		return "", fmt.Errorf("unsupported PPT runner profile %q", value)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
