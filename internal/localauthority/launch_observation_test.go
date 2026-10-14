//go:build darwin || linux

package localauthority

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

func nativeObservationFixtureFrame() nativeObservationFrame {
	return nativeObservationFrame{Schema: 1, Protocol: nativeObservationProtocol, Binding: hash([]byte("synthetic-launch-binding")), Ordinal: 1, ProjectRef: observability.BindLocalProject(observability.WithLocalProjectRelay(context.Background(), observability.NewLocalProjectRelay([]string{string(testProject)})), string(testProject)).ProjectRef(), Code: observability.LocalAuthorityCommitted, Phase: "authority_commit", Outcome: "committed"}
}

func TestNativeObservationWireClosedAndBounded(t *testing.T) {
	f := nativeObservationFixtureFrame()
	body, _ := json.Marshal(f)
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	decoded, err := readNativeObservation(bytes.NewReader(append(header[:], body...)))
	if err != nil || decoded != f || len(body) > nativeObservationFrameLimit {
		t.Fatal("bounded closed native observation did not roundtrip")
	}
	for _, bad := range []string{`{}`, `{"schema":1,"schema":1}`, `{"project_ref":"synthetic-private"}`, string(body) + `{}`, strings.Replace(string(body), `"schema":`, `"Schema":`, 1), strings.Replace(string(body), `"sealed":false`, `"sealed":false,"path":"synthetic-private"`, 1)} {
		binary.BigEndian.PutUint32(header[:], uint32(len(bad)))
		if _, err := readNativeObservation(bytes.NewReader(append(header[:], bad...))); err == nil {
			t.Fatal("native observation accepted open, duplicate or suffixed grammar")
		}
	}
	binary.BigEndian.PutUint32(header[:], nativeObservationFrameLimit+1)
	if _, err := readNativeObservation(bytes.NewReader(header[:])); err == nil {
		t.Fatal("native observation allocated an oversized payload")
	}
	for _, change := range []func(*nativeObservationFrame){
		func(f *nativeObservationFrame) { f.Protocol = "other" },
		func(f *nativeObservationFrame) { f.Ordinal = 0 },
		func(f *nativeObservationFrame) { f.Ordinal = nativeObservationMaxOrdinal + 1 },
		func(f *nativeObservationFrame) { f.Lost = observability.LocalProjectRelayMaxLoss + 1 },
		func(f *nativeObservationFrame) { f.ProjectRef = string(testProject) },
		func(f *nativeObservationFrame) { f.Code = observability.SyncCommitted },
		func(f *nativeObservationFrame) { f.Outcome = "failed" },
		func(f *nativeObservationFrame) { f.Sealed = true },
	} {
		bad := f
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("native observation widened the finite contract")
		}
	}
}

func TestNativeObservationRetainedAdmissionRejectsReplayAndForeignCohort(t *testing.T) {
	c := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "relay-evidence"), ManagedOutput: true})
	resume, err := c.PauseWriterForQualification(context.Background())
	if err != nil {
		t.Fatal("actual retained collector idle boundary unavailable")
	}
	defer resume()
	defer c.Close(context.Background(), true)
	ctx := observability.WithObservation(context.Background(), c, observability.Context{})
	r := newNativeObservationReader(ctx, nil, []launchProject{{ProjectID: testProject}})
	f := nativeObservationFixtureFrame()
	r.binding = f.Binding
	for _, change := range []func(*nativeObservationFrame){
		func(f *nativeObservationFrame) { f.Binding = hash([]byte("other runtime")) },
		func(f *nativeObservationFrame) {
			f.ProjectRef = observability.BindLocalProject(ctx, "foreign").ProjectRef()
		},
		func(f *nativeObservationFrame) { f.Ordinal = 2 },
	} {
		bad := f
		change(&bad)
		if r.admit(bad) || r.ordinal != 0 || r.lost != 0 {
			t.Fatal("rejected relay frame altered admission state")
		}
	}
	f.Lost = 7
	if !r.admit(f) || r.admit(f) {
		t.Fatal("original relay boundary or exact replay admission changed")
	}
	f.Ordinal, f.Lost = 2, 6
	if r.admit(f) {
		t.Fatal("native loss counter rollback accepted")
	}
	seal := nativeObservationFrame{Schema: 1, Protocol: nativeObservationProtocol, Binding: r.binding, Ordinal: 2, Lost: 7, Sealed: true}
	if !r.admit(seal) || r.admit(seal) {
		t.Fatal("terminal relay evidence was absent or reusable")
	}
	if !resume() || !c.Close(context.Background(), true) {
		t.Fatal("relay evidence writer did not join")
	}
	page, err := c.Query(observability.Filter{ProjectRef: f.ProjectRef}, "", 200)
	if err != nil || len(page.Events) != 1 || page.Coverage.Dropped != 7 || page.Events[0].CorrelationRef == "" || page.Events[0].RepoRef != "" {
		t.Fatal("retained native admission lost finite identity or counted loss")
	}
}

func TestNativeObservationStreamSealAndDisconnect(t *testing.T) {
	for _, clean := range []bool{true, false} {
		t.Run(map[bool]string{true: "sealed", false: "disconnected"}[clean], func(t *testing.T) {
			parent, child := launchPair(t)
			c := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "relay-stream"), ManagedOutput: true})
			resume, err := c.PauseWriterForQualification(context.Background())
			if err != nil {
				t.Fatal("relay stream actual collector idle boundary unavailable")
			}
			defer resume()
			defer c.Close(context.Background(), true)
			ctx := observability.WithObservation(context.Background(), c, observability.Context{})
			r := newNativeObservationReader(ctx, parent, []launchProject{{ProjectID: testProject}})
			f := nativeObservationFixtureFrame()
			r.binding = f.Binding
			done := make(chan struct{})
			go func() { defer close(done); r.Receive() }()
			if writeNativeObservation(child, f, time.Time{}) != nil {
				t.Fatal("closed content-free stream transfer failed")
			}
			if clean {
				seal := nativeObservationFrame{Schema: 1, Protocol: nativeObservationProtocol, Binding: r.binding, Ordinal: 2, Sealed: true}
				if writeNativeObservation(child, seal, time.Time{}) != nil {
					t.Fatal("native stream seal transfer failed")
				}
			}
			_ = child.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("native observation reader did not join")
			}
			if !resume() || !c.Close(context.Background(), true) {
				t.Fatal("native stream evidence did not join")
			}
			page, err := c.Query(observability.Filter{ProjectRef: f.ProjectRef}, "", 200)
			expectedLoss := uint64(1)
			if clean {
				expectedLoss = 0
			}
			if err != nil || len(page.Events) != 1 || page.Coverage.Dropped != expectedLoss {
				t.Fatal("native stream invented completion or lost confirmed evidence")
			}
		})
	}
}

func TestNativeObservedHandshakeCancellationAndChannelLossPreserveAuthority(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained", true: "channel_loss"}[disconnect], func(t *testing.T) {
			launch, file, _, _ := launchFixture(t)
			parent, child := launchPair(t)
			collector := observability.NewQualificationCollector(observability.Config{Directory: filepath.Join(privateTestDirectory(t), "handshake-evidence"), ManagedOutput: true})
			resume, err := collector.PauseWriterForQualification(context.Background())
			if err != nil {
				t.Fatal("actual handshake evidence writer idle boundary unavailable")
			}
			defer resume()
			defer collector.Close(context.Background(), true)
			observed := observability.WithObservation(context.Background(), collector, observability.Context{})
			startup, stop := context.WithTimeout(observed, 2*time.Second)
			defer stop()
			type result struct {
				reader *NativeObservationReader
				err    error
			}
			sent := make(chan result, 1)
			go func() { r, err := launch.SendObserved(startup, parent); sent <- result{r, err} }()
			received, err := ReceiveLaunch(startup, file, child)
			if err != nil {
				t.Fatal("observed launch receive failed")
			}
			defer received.Close(context.Background())
			core, err := received.Activate(context.Background(), startup)
			if err != nil {
				t.Fatal("observed exact cohort activation failed")
			}
			transfer := <-sent
			if transfer.err != nil || transfer.reader == nil || received.frame.Observation != nativeObservationProtocol {
				t.Fatal("explicit retained observation silently downgraded")
			}
			if on, err := received.StartObservations(); err != nil || !on {
				t.Fatal("observed bootstrap deadline handoff failed")
			}
			if disconnect {
				stop()
			} else {
				<-startup.Done() // both actual startup FD deadlines must be cleared
			}
			done := make(chan struct{})
			go func() { defer close(done); transfer.reader.Receive() }()
			if disconnect {
				_ = parent.Close()
			}
			req := request(t, "issue.create", "synthetic-observed-handshake", authority.IssuePayload{Title: "Confirmed despite observation channel state", State: "open"})
			receipt, err := core.Mutate(context.Background(), req, testActor)
			if err != nil || receipt.Validate() != nil {
				t.Fatal("observation lifetime changed confirmed canonical source")
			}
			if replay, err := core.Mutate(context.Background(), req, testActor); err != nil || !replay.Replayed || replay.Citation != receipt.Citation {
				t.Fatal("observation failure changed idempotency")
			}
			if received.Close(context.Background()) != nil {
				t.Fatal("observation channel failure changed joined authority shutdown")
			}
			_ = child.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("observed handshake reader did not join")
			}
			if !resume() || !collector.Close(context.Background(), true) {
				t.Fatal("observed handshake retention did not join")
			}
			ref := observability.BindLocalProject(observed, string(testProject)).ProjectRef()
			page, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityCommitted, ProjectRef: ref}, "", 200)
			if err != nil {
				t.Fatal("observed handshake evidence unavailable")
			}
			if disconnect {
				if len(page.Events) != 0 || page.Coverage.Dropped == 0 {
					t.Fatal("disconnected relay invented complete retained evidence")
				}
			} else if len(page.Events) != 1 || page.Coverage.Dropped != 0 {
				t.Fatal("cancelled startup deadline leaked into retained observation lifetime")
			}
			if !disconnect {
				runtime, err := collector.Query(observability.Filter{Code: observability.LocalAuthorityPhaseChanged, ProjectRef: ref, Phase: "runtime"}, "", 200)
				// Activation without a listener must not invent serving.
				if err != nil || len(runtime.Events) != 2 || runtime.Events[0].Outcome != "closing" || runtime.Events[1].Outcome != "stopped" {
					t.Fatal("activated owner without a listener lost joined shutdown proof")
				}
			}
		})
	}
}

func TestNativeObservationWriterDisconnectIsBounded(t *testing.T) {
	parent, child := launchPair(t)
	_ = parent.Close()
	r := observability.NewLocalProjectRelay([]string{string(testProject)})
	o := observability.BindLocalProject(observability.WithLocalProjectRelay(context.Background(), r), string(testProject))
	for i := 0; i < observability.LocalProjectRelayCapacity+1; i++ {
		o.SourceCommitted()
	}
	r.Seal()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runNativeObservation(child, r, hash([]byte("synthetic-disconnected-binding")))
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected native observation writer retained owner lifetime")
	}
}
