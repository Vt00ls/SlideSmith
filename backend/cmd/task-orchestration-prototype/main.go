// Command task-orchestration-prototype is a throwaway TUI for issue #19.
// It is not a production entry point.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	prototype "github.com/slidesmith/slidesmith/backend/internal/service/task_orchestration_prototype"
)

func main() {
	scenario := flag.String("scenario", "", "run a repeatable scenario (compare)")
	flag.Parse()
	if *scenario == "compare" {
		runComparison()
		return
	}
	runTUI()
}

func runComparison() {
	results := make([]prototype.Comparison, 0, 2)
	for _, engine := range []prototype.Engine{prototype.CommandDecisionEngine{}, prototype.EventEnactmentEngine{}} {
		_, result := prototype.RunComparison(engine)
		results = append(results, result)
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	fmt.Println(string(data))
}

func runTUI() {
	engines := []prototype.Engine{prototype.CommandDecisionEngine{}, prototype.EventEnactmentEngine{}}
	engineIndex := 0
	route := prototype.GenerationRoute
	state := prototype.NewSnapshot()
	reader := bufio.NewReader(os.Stdin)
	sequence := 0

	for {
		render(engines[engineIndex], route, state)
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		key := strings.TrimSpace(line)
		if key == "q" {
			return
		}
		if key == "x" {
			engineIndex = (engineIndex + 1) % len(engines)
			state = prototype.NewSnapshot()
			continue
		}
		if key == "o" {
			route = nextRoute(route)
			continue
		}
		if key == "z" {
			state = prototype.NewSnapshot()
			continue
		}
		if key == "a" {
			state, _ = prototype.RunComparison(engines[engineIndex])
			continue
		}
		kind, ok := triggerForKey(key)
		if !ok {
			continue
		}
		sequence++
		expected := state.Revision
		if key == "s" {
			expected--
		}
		state = engines[engineIndex].Apply(state, prototype.Trigger{
			Kind: kind, IdempotencyKey: fmt.Sprintf("interactive-%03d", sequence),
			ExpectedRevision: expected, Route: route, ArtifactVersion: state.LatestArtifactVersion,
		})
	}
}

func render(engine prototype.Engine, route prototype.Route, state prototype.Snapshot) {
	fmt.Print("\033[2J\033[H")
	fmt.Printf("\033[1mPROTOTYPE — Task Orchestration #19\033[0m\n")
	fmt.Printf("\033[2mshape=%s  selected-route=%s\033[0m\n\n", engine.Name(), route)
	fmt.Printf("\033[1mTask\033[0m id=%s rev=%d status=%s activity=%s route=%s cursor=%d\n",
		state.TaskID, state.Revision, state.Status, state.Activity, state.Route, state.CurrentPhaseIndex)
	fmt.Printf("\033[1mPins\033[0m pipeline=%s runtime=%s template=%s\n",
		state.PipelineVersion, state.RuntimeRelease, state.TemplateLock)
	fmt.Printf("\033[1mWorkspace\033[0m revision=%s checkpoint=%s artifact=%s\n",
		state.WorkspaceRevision, state.Checkpoint, state.LatestArtifactVersion)
	fmt.Printf("\033[1mHistory\033[0m phase-runs=%d active=%s\n", len(state.PhaseRuns), state.ActivePhaseRunID)
	for _, run := range state.PhaseRuns {
		fmt.Printf("  %s %s#%d outcome=%s runtime=%d validation=%s commit=%s cancel=%t\n",
			run.ID, run.PhaseKey, run.Attempt, run.Outcome, len(run.RuntimeRuns),
			run.ValidationEvidence, run.WorkspaceCommit, run.CancellationRequested)
	}
	pending := 0
	for _, effect := range state.Outbox {
		if effect.Status == "pending" {
			pending++
			fmt.Printf("  \033[1mpending\033[0m %s %s phase-run=%s\n", effect.ID, effect.Kind, effect.PhaseRunID)
		}
	}
	fmt.Printf("\033[1mBoundary evidence\033[0m journal=%d idempotency=%d pending=%d rejected-facts=%d\n",
		len(state.Journal), len(state.IdempotencyRecords), pending, state.RejectedAuthoritativeFacts)
	fmt.Printf("\033[1mLast outcome\033[0m %s\n", state.LastOutcome)
	fmt.Println("\n\033[1mActions\033[0m")
	fmt.Println("[o] route  [b] start  [w] worker tick  [l] claim lost  [r] runtime ok  [f] runtime fail")
	fmt.Println("[v] validation ok  [n] validation fail  [c] workspace commit  [g] confirm  [p] publish")
	fmt.Println("[k] cancel  [d] cancellation fenced  [y] retry  [e] reconcile  [m] manual edit")
	fmt.Println("[s] stale worker tick  [x] switch shape + reset  [a] comparison scenario  [z] reset  [q] quit")
	fmt.Print("\n> ")
}

func nextRoute(route prototype.Route) prototype.Route {
	switch route {
	case prototype.GenerationRoute:
		return prototype.BeautifyRoute
	case prototype.BeautifyRoute:
		return prototype.TemplateFillRoute
	default:
		return prototype.GenerationRoute
	}
}

func triggerForKey(key string) (prototype.TriggerKind, bool) {
	triggers := map[string]prototype.TriggerKind{
		"b": prototype.StartTask,
		"w": prototype.WorkerTick,
		"l": prototype.WorkerClaimLost,
		"r": prototype.RuntimeSucceeded,
		"f": prototype.RuntimeFailed,
		"v": prototype.ValidationSucceeded,
		"n": prototype.ValidationFailed,
		"c": prototype.WorkspaceCommitted,
		"g": prototype.ConfirmationApproved,
		"p": prototype.PublicationSucceeded,
		"k": prototype.CancelTask,
		"d": prototype.CancellationFenced,
		"y": prototype.RetryPhase,
		"e": prototype.ReconcileTask,
		"m": prototype.StartManualEdit,
		"s": prototype.WorkerTick,
	}
	kind, ok := triggers[key]
	return kind, ok
}
