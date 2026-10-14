//go:build darwin || linux

package localauthority_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

func TestNativeProductObservedSourceRetainedAcrossRestartAndReplay(t *testing.T) {
	executable := buildNativeProduct(t)
	p, _, dir := nativeLaunchFixture(t)
	ledger := filepath.Join(privateTemp(t), "native-observations")
	request := nativeMutation("synthetic-private-native-observation-key", "Synthetic private native title")
	var original authority.MutationReceipt
	var originalEvent observability.Event
	for attempt := 0; attempt < 2; attempt++ {
		collector := observability.NewQualificationCollector(observability.Config{Directory: ledger, ManagedOutput: true})
		t.Cleanup(func() { collector.Close(context.Background(), true) })
		resume, err := collector.PauseWriterForQualification(context.Background())
		if err != nil {
			t.Fatal("native retained writer idle boundary unavailable")
		}
		defer resume()
		observed := observability.WithObservation(context.Background(), collector, observability.Context{Repo: observability.KnownRef("repo", "unrelated-native-parent")})
		child := spawnProductChildObserved(t, executable, p, dir, "", observed)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var receipt authority.MutationReceipt
		if child.client.Call(ctx, "AuthorityV1.Mutate", request, &receipt) != nil || receipt.Validate() != nil || receipt.Replayed != (attempt == 1) {
			cancel()
			t.Fatal("observed actual product changed canonical commit/replay")
		}
		if attempt == 0 {
			original = receipt
		} else if receipt.Citation != original.Citation {
			t.Fatal("observed product restart changed canonical citation")
		}
		if child.cmd.Process.Signal(syscall.SIGTERM) != nil {
			t.Fatal("observed native child did not accept owned stop")
		}
		select {
		case <-child.done:
		case <-ctx.Done():
			t.Fatal("observed actual product/parent reader did not join")
		}
		cancel()
		if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Success() || !resume() || !collector.Close(context.Background(), true) {
			t.Fatal("actual native retained observation did not seal")
		}
		refs := observability.ProjectEntityRefs(observability.Context{Project: observability.KnownRef("project", string(fixtureProject))})
		page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityCommitted, ProjectRef: refs.Project}, "", 200)
		if err != nil || len(page.Events) != 1 || page.Coverage.Dropped != 0 {
			t.Fatal("native retained delivery/reload duplicated or lost actual owner proof")
		}
		event := page.Events[0]
		if event.Code != observability.LocalAuthorityCommitted || event.Phase != "authority_commit" || event.Outcome != "committed" || event.RepoRef != "" || event.JobRef != "" || event.CorrelationRef == "" {
			t.Fatal("native retained proof acquired foreign scope or arbitrary correlation")
		}
		if attempt == 0 {
			originalEvent = event
		} else if event != originalEvent {
			t.Fatal("parent ledger reload altered the original retained boundary")
		}
		runtime, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityPhaseChanged, ProjectRef: refs.Project, Phase: "runtime"}, "", 200)
		if err != nil || len(runtime.Events) != 3*(attempt+1) || runtime.Coverage.Dropped != 0 {
			t.Fatal("native runtime lifecycle was lost or incorrectly replayed")
		}
		for i, phase := range runtime.Events {
			if phase.Outcome != []string{"serving", "closing", "stopped"}[i%3] {
				t.Fatal("actual native owner changed confirmed runtime boundaries")
			}
		}
		body, _ := json.Marshal(page)
		for _, private := range []string{dir, ledger, string(fixtureProject), string(original.Citation.EntityID), "synthetic-private-native", "Synthetic private native", "unrelated-native-parent"} {
			if strings.Contains(string(body), private) {
				t.Fatal("native retained proof exposed protected source or bootstrap state")
			}
		}
	}
}

func TestNativeObservedProcessCloseJoinsRetainedReader(t *testing.T) {
	p, _, dir := nativeLaunchFixture(t)
	collector := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTemp(t), "parent-process-observations"), ManagedOutput: true})
	t.Cleanup(func() { collector.Close(context.Background(), true) })
	resume, err := collector.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("parent native collector idle boundary unavailable")
	}
	defer resume()
	observed := observability.WithObservation(context.Background(), collector, observability.Context{})
	process := nativeProcessObserved(t, p, dir, observed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var receipt authority.MutationReceipt
	if process.Client().Call(ctx, "AuthorityV1.Mutate", nativeMutation("synthetic-observed-parent-process", "Owned parent source"), &receipt) != nil || receipt.Validate() != nil {
		t.Fatal("observed parent process source commit failed")
	}
	if process.Close(ctx) != nil || !resume() || !collector.Close(context.Background(), true) {
		t.Fatal("parent process did not join reader before collector seal")
	}
	ref := observability.BindLocalProject(observed, string(fixtureProject)).ProjectRef()
	page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityCommitted, ProjectRef: ref}, "", 200)
	if err != nil || len(page.Events) != 1 || page.Coverage.Dropped != 0 {
		t.Fatal("parent process close overtook final retained kernel buffers")
	}
	runtime, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityPhaseChanged, ProjectRef: ref, Phase: "runtime"}, "", 200)
	if err != nil || len(runtime.Events) != 3 || runtime.Coverage.Dropped != 0 {
		t.Fatal("parent process did not join final runtime lifecycle proof")
	}
	for i, phase := range runtime.Events {
		if phase.Outcome != []string{"serving", "closing", "stopped"}[i] {
			t.Fatal("parent process changed actual runtime lifecycle")
		}
	}
}
