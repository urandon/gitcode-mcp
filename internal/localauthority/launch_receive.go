package localauthority

import (
	"context"
	"net"
	"os"
	"sync"

	"gitcode-mcp/internal/authority"
)

// ReceivedLaunch is private bootstrap state, not serialized permission to
// select arbitrary ownership. Its cohort is immutable and existing-only.
type ReceivedLaunch struct {
	mu         sync.Mutex
	prepared   *PreparedRegistry
	bindings   []ProjectBinding
	frame      launchFrame
	conn       *net.UnixConn
	core       *Coordinator
	stop       context.Context
	cancel     context.CancelFunc
	parentStop func() bool
	used       bool
}

// ReceiveLaunch consumes an inherited private registry role only through an
// authenticated connected Unix channel. It performs no account/environment,
// setup or migration fallback. The caller retains ownership on refusal.
func ReceiveLaunch(ctx context.Context, directory *os.File, conn *net.UnixConn) (*ReceivedLaunch, error) {
	if validateLaunchDirectory(directory) != nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	ctx, release, err := launchHandshake(ctx, conn)
	if err != nil {
		return nil, err
	}
	defer release()
	challenge, err := authority.NewUUID()
	if err != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	if err = writeLaunch(conn, launchMessage{Schema: 1, Protocol: authority.Protocol, Profile: strictLaunchProfile, Phase: "challenge", Challenge: challenge}); err != nil {
		return nil, err
	}
	var f launchFrame
	if err = readLaunch(conn, &f); err != nil || f.Challenge != challenge {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	identity, err := physicalIdentity(directory)
	if err != nil || identity != f.Identity {
		return nil, authority.Error{Code: authority.Conflict}
	}
	if err = (runtimeRegistry{directory, identity}).verify(f.Root); err != nil {
		return nil, err
	}
	// Requalify the ENTIRE exact cohort/full binding before any writable open.
	bindings := make([]ProjectBinding, 0, len(f.Projects))
	scope := make(map[authority.UUID]launchProject, len(f.Projects))
	for _, expected := range f.Projects {
		b := ProjectBinding{expected.ProjectID, expected.Directory}
		actual, err := inspectLaunchProject(ctx, directory, b)
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, authority.Error{Code: authority.Conflict}
		}
		bindings, scope[b.ProjectID] = append(bindings, b), expected
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	p := &PreparedRegistry{registry: &Registry{root: f.Root, runtime: &runtimeRegistry{directory, identity}}, directory: directory, identity: identity, scope: scope}
	stop, cancel := context.WithCancel(context.Background())
	return &ReceivedLaunch{prepared: p, bindings: bindings, frame: f, conn: conn, stop: stop, cancel: cancel}, nil
}

// Activate acquires the exact existing cohort, then performs READY -> ACTIVATE
// -> ACTIVE. Only a successful return permits serving operations. A caller must
// validate its separate listener role before calling this method.
func (r *ReceivedLaunch) Activate(lifetime, handshake context.Context) (*Coordinator, error) {
	if r == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if handshake.Err() != nil {
		return nil, handshake.Err()
	}
	if !r.mu.TryLock() {
		return nil, authority.Error{Code: authority.Conflict}
	}
	defer r.mu.Unlock()
	if r.used || r.prepared == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if r.stop.Err() != nil || lifetime.Err() != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	r.used = true
	activation, cancelActivation := context.WithCancel(handshake)
	stopClose := context.AfterFunc(r.stop, cancelActivation)
	stopLifetime := context.AfterFunc(lifetime, cancelActivation)
	defer func() { stopClose(); stopLifetime(); cancelActivation() }()
	ctx, release, err := launchHandshake(activation, r.conn)
	if err != nil {
		return nil, err
	}
	defer release()
	admission, cancel := context.WithCancel(r.stop)
	r.parentStop = context.AfterFunc(lifetime, cancel)
	stopAdmission := context.AfterFunc(ctx, cancel)
	defer stopAdmission()
	r.core, err = newStrictCoordinator(admission, r.prepared, r.bindings, false)
	if err != nil {
		cancel()
		r.parentStop()
		return nil, err
	}
	binding := launchBinding(r.frame)
	if err = writeLaunch(r.conn, launchReply(r.frame, binding, "ready")); err == nil {
		err = expectLaunch(r.conn, r.frame, binding, "activate")
	}
	if err == nil {
		if ctx.Err() != nil || r.stop.Err() != nil || !r.core.Available() {
			err = authority.Error{Code: authority.Unavailable}
		}
	}
	if err == nil {
		err = writeLaunch(r.conn, launchReply(r.frame, binding, "active"))
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (r.stop.Err() != nil || !r.core.Available()) {
		err = authority.Error{Code: authority.Unavailable}
	}
	if err != nil {
		// Ownership remains in r until Close has actually joined the core.
		cancel()
		r.parentStop()
		return nil, err
	}
	if err = r.core.activateRecoveryWorkers(); err != nil {
		cancel()
		r.parentStop()
		return nil, err
	}
	return r.core, nil
}

func (r *ReceivedLaunch) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	// Seal admission synchronously even when the caller supplied no remaining
	// join budget. Cancellation is immutable after construction and safe while
	// Activate holds the admission mutex; retained ownership is joined later.
	if r.cancel != nil {
		r.cancel()
	}
	if !r.mu.TryLock() {
		return authority.Error{Code: authority.Conflict}
	}
	defer r.mu.Unlock()
	if r.parentStop != nil {
		r.parentStop()
	}
	if r.core != nil {
		if err := r.core.Close(ctx); err != nil {
			return err
		}
	}
	if r.prepared != nil {
		return r.prepared.Close()
	}
	return nil
}
