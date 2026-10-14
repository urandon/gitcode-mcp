package localauthority

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/observability"
)

const nativeObservationProtocol = "local_project_observation.v1"
const nativeObservationFrameLimit = 1024
const nativeObservationIOBudget = time.Second
const nativeObservationMaxOrdinal = uint64(1<<32 - 1)

// This private closed frame is never a general Event or execution request.
// Binding authenticates one launch epoch; only opaque cohort refs cross it.
type nativeObservationFrame struct {
	Schema     int                `json:"schema"`
	Protocol   string             `json:"protocol"`
	Binding    string             `json:"binding"`
	Ordinal    uint64             `json:"ordinal"`
	Lost       uint64             `json:"lost"`
	Sealed     bool               `json:"sealed"`
	ProjectRef string             `json:"project_ref,omitempty"`
	Code       observability.Code `json:"code,omitempty"`
	Phase      string             `json:"phase,omitempty"`
	Outcome    string             `json:"outcome,omitempty"`
}

func (f *nativeObservationFrame) Validate() error {
	if f.Schema != 1 || f.Protocol != nativeObservationProtocol || !validDigest(f.Binding) || f.Ordinal == 0 || f.Ordinal > nativeObservationMaxOrdinal || f.Lost > observability.LocalProjectRelayMaxLoss {
		return authority.Error{Code: authority.InvalidContract}
	}
	if f.Sealed {
		if f.ProjectRef != "" || f.Code != "" || f.Phase != "" || f.Outcome != "" {
			return authority.Error{Code: authority.InvalidContract}
		}
	} else if !(observability.LocalProjectTransition{ProjectRef: f.ProjectRef, Code: f.Code, Phase: f.Phase, Outcome: f.Outcome}).Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func readNativeObservation(r io.Reader) (nativeObservationFrame, error) {
	var f nativeObservationFrame
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return f, authority.Error{Code: authority.Unavailable}
	}
	n := binary.BigEndian.Uint32(header[:])
	if n < 2 || n > nativeObservationFrameLimit {
		return f, authority.Error{Code: authority.InvalidContract}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return f, authority.Error{Code: authority.Unavailable}
	}
	err := authority.DecodeJSON(body, &f)
	return f, err
}

func writeNativeObservation(conn *net.UnixConn, f nativeObservationFrame, drainDeadline time.Time) error {
	if f.Validate() != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	body, err := json.Marshal(f)
	if err != nil || len(body) > nativeObservationFrameLimit {
		return authority.Error{Code: authority.InvalidContract}
	}
	deadline := time.Now().Add(nativeObservationIOBudget)
	if !drainDeadline.IsZero() && drainDeadline.Before(deadline) {
		deadline = drainDeadline
	}
	if conn.SetWriteDeadline(deadline) != nil {
		return authority.Error{Code: authority.Unavailable}
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	frame := append(header[:], body...)
	for len(frame) > 0 {
		n, err := conn.Write(frame)
		if err != nil || n <= 0 {
			return authority.Error{Code: authority.Unavailable}
		}
		frame = frame[n:]
	}
	return nil
}

// runNativeObservation is the only child I/O consumer, never called under a
// source/ledger turn. A failed channel omits diagnostics, not source work.
func runNativeObservation(conn *net.UnixConn, relay *observability.LocalProjectRelay, binding string) {
	ordinal := uint64(0)
	var drainDeadline time.Time
	for {
		select {
		case <-relay.Sealed():
			if drainDeadline.IsZero() {
				drainDeadline = time.Now().Add(nativeObservationIOBudget)
			}
		default:
		}
		if ordinal == nativeObservationMaxOrdinal {
			return // cannot qualify another ordinal; parent requires a seal
		}
		transition, ok := <-relay.Events()
		ordinal++
		f := nativeObservationFrame{Schema: 1, Protocol: nativeObservationProtocol, Binding: binding, Ordinal: ordinal, Lost: relay.Lost(), Sealed: !ok}
		if ok {
			f.ProjectRef, f.Code, f.Phase, f.Outcome = transition.ProjectRef, transition.Code, transition.Phase, transition.Outcome
		}
		if writeNativeObservation(conn, f, drainDeadline) != nil || !ok {
			return
		}
	}
}

// NativeObservationReader owns only the retained parent sink and exact opaque
// launch cohort. It cannot route remote entities, execute operations, or read
// source files. One process-lifetime goroutine calls Receive; exit closes the
// channel and joins it before publishing process done.
type NativeObservationReader struct {
	conn      *net.UnixConn
	binding   string
	observers map[string]observability.LocalProjectObserver
	lossOwner observability.LocalProjectObserver
	ordinal   uint64
	lost      uint64
	sealed    bool
}

func newNativeObservationReader(ctx context.Context, conn *net.UnixConn, projects []launchProject) *NativeObservationReader {
	r := &NativeObservationReader{conn: conn, observers: map[string]observability.LocalProjectObserver{}}
	for _, project := range projects {
		o := observability.BindLocalProject(ctx, string(project.ProjectID))
		if !o.Retained() {
			return nil
		}
		r.observers[o.ProjectRef()] = o
		r.lossOwner = o
	}
	if len(r.observers) == 0 {
		return nil
	}
	return r
}

func (r *NativeObservationReader) admit(f nativeObservationFrame) bool {
	if r.sealed || f.Validate() != nil || f.Binding != r.binding || r.ordinal == nativeObservationMaxOrdinal || f.Ordinal != r.ordinal+1 || f.Lost < r.lost {
		return false
	}
	var o observability.LocalProjectObserver
	if !f.Sealed {
		o = r.observers[f.ProjectRef]
		if !o.Retained() {
			return false
		}
	}
	r.lossOwner.UnconfirmedCommits(f.Lost - r.lost)
	r.ordinal, r.lost = f.Ordinal, f.Lost
	if f.Sealed {
		r.sealed = true
		return true
	}
	switch f.Code {
	case observability.LocalAuthorityCommitted:
		if f.Phase == "authority_commit" {
			o.SourceCommitted()
		} else {
			o.DocumentCommitted()
		}
	case observability.LocalAuthorityPhaseChanged:
		o.PhaseChanged(f.Phase, f.Outcome)
	}
	return true
}

// Receive uses the deadline handoff already completed by SendObserved. An idle
// healthy owner has no read timeout. Real exit imposes a bounded kernel-buffer
// drain before closing this FD, preserving any already transferred final seal.
// Missing/invalid terminal evidence is partial coverage, never owner health.
func (r *NativeObservationReader) Receive() {
	if r == nil {
		return
	}
	defer func() {
		if !r.sealed {
			r.lossOwner.UnconfirmedCommit()
		}
	}()
	for !r.sealed {
		f, err := readNativeObservation(r.conn)
		if err != nil || !r.admit(f) {
			return
		}
	}
}
