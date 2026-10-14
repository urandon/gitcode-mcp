package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"

	"gitcode-mcp/internal/authority"
)

const (
	semanticBirthFile           = "runtime-semantic-birth.json"
	semanticEnrollmentFile      = "runtime-semantic-enrollment.json"
	semanticControlFile         = "runtime-semantic-control.json"
	semanticVectorDirectory     = "runtime-semantic-vectors"
	semanticQuarantineDirectory = "runtime-semantic-quarantine"
	semanticRootEntries         = 64
	semanticControlBytes        = 512 << 10
	semanticControlStages       = 4
	semanticControlStageBytes   = 2 << 20
	semanticSmallControlBytes   = 8 << 10
	semanticQuarantineBytes     = 64 << 20
)

// Private, transient role selectors and retained ownership proof. This is not
// a public path/inventory upload, a namespace constructor, or proof of content
// eligibility. The durable owner must supply its independently qualified
// preparation/control inventory; names alone never grant filesystem ownership.
type semanticPhysicalProof struct {
	Identity  string
	Digest    string
	Bytes     int64
	Retained  bool   // generation classification from original control, not a name
	Namespace string // original namespace for a quarantined generation
}
type semanticPhysicalInventory struct {
	binding semanticQuotaBinding
	proofs  map[string]semanticPhysicalProof
}

// Local source-mutex contention is not a physical corruption observation and
// does not create a persistent semantic fence. No scan image is qualified.
type semanticPhysicalBusy struct{}

func (semanticPhysicalBusy) Error() string { return "semantic physical inspection is busy" }

type semanticPhysicalRole struct {
	directory  bool
	cap        int64
	kind       string
	generation string
	namespace  string
}

func semanticUUIDName(name, prefix, suffix string) bool {
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) && authority.UUID(strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)).Valid()
}

// The grammar has no absolute coordinates, traversal, arbitrary leaf names or
// depth beyond four fixed role components. Canonical SQL/Git are not roles.
func semanticRole(path string) (semanticPhysicalRole, bool) {
	var zero semanticPhysicalRole
	p := strings.Split(path, "/")
	if len(p) < 1 || len(p) > 4 {
		return zero, false
	}
	for _, part := range p {
		if part == "" || len(part) > 128 || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return zero, false
		}
	}
	if len(p) == 1 {
		switch p[0] {
		case semanticBirthFile, semanticEnrollmentFile:
			return semanticPhysicalRole{cap: semanticSmallControlBytes, kind: "anchor"}, true
		case semanticControlFile:
			return semanticPhysicalRole{cap: semanticControlBytes, kind: "control"}, true
		case semanticVectorDirectory, semanticQuarantineDirectory:
			return semanticPhysicalRole{directory: true, kind: "root"}, true
		}
		if semanticUUIDName(p[0], "runtime-semantic-stage-", ".json") {
			return semanticPhysicalRole{cap: semanticControlBytes, kind: "control_stage"}, true
		}
		return zero, false
	}
	var generation, namespace string
	if p[0] == semanticVectorDirectory && strings.HasPrefix(p[1], "ns-") && validDigest(strings.TrimPrefix(p[1], "ns-")) {
		namespace = p[1]
		if len(p) == 2 {
			return semanticPhysicalRole{directory: true, kind: "namespace", namespace: namespace}, true
		}
		if len(p) == 3 && (p[2] == "head.json" || semanticUUIDName(p[2], "head-stage-", ".json")) {
			kind := "head"
			if p[2] != "head.json" {
				kind = "head_stage"
			}
			return semanticPhysicalRole{cap: 32 << 10, kind: kind, namespace: namespace}, true
		}
		if !semanticUUIDName(p[2], "gen-", "") {
			return zero, false
		}
		generation = strings.Join(p[:3], "/")
		if len(p) == 3 {
			return semanticPhysicalRole{directory: true, kind: "generation", generation: generation, namespace: namespace}, true
		}
	} else if p[0] == semanticQuarantineDirectory && semanticUUIDName(p[1], "retired-", "") {
		generation = strings.Join(p[:2], "/")
		if len(p) == 2 {
			return semanticPhysicalRole{directory: true, kind: "quarantine_generation", generation: generation}, true
		}
		if len(p) != 3 {
			return zero, false
		}
	} else {
		return zero, false
	}
	leaf := p[len(p)-1]
	if leaf == "manifest.json" {
		return semanticPhysicalRole{cap: semanticManifestBytes, kind: "manifest", generation: generation, namespace: namespace}, true
	}
	if len(leaf) == len("shard-00.bin") && strings.HasPrefix(leaf, "shard-") && strings.HasSuffix(leaf, ".bin") {
		n, err := strconv.Atoi(leaf[6:8])
		if err == nil && n >= 0 && n < semanticShards && leaf[6:8] == fmtSemanticOrdinal(n) {
			return semanticPhysicalRole{cap: semanticShardBytes, kind: "shard", generation: generation, namespace: namespace}, true
		}
	}
	return zero, false
}

func fmtSemanticOrdinal(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func (i semanticPhysicalInventory) snapshot(scope *semanticQuotaScope) (semanticPhysicalInventory, error) {
	if scope == nil || i.binding != scope.binding || len(i.proofs) > semanticProjectEntries {
		return semanticPhysicalInventory{}, authority.Error{Code: authority.InvalidContract}
	}
	next := semanticPhysicalInventory{i.binding, make(map[string]semanticPhysicalProof, len(i.proofs))}
	for name, p := range i.proofs {
		r, ok := semanticRole(name)
		if !ok || !validDigest(p.Identity) || p.Bytes < 0 || r.directory && (p.Bytes != 0 || p.Digest != "") || !r.directory && (!validDigest(p.Digest) || p.Bytes > r.cap) || p.Retained && r.kind != "generation" || r.kind == "quarantine_generation" && !validDigest(p.Namespace) || r.kind != "quarantine_generation" && p.Namespace != "" {
			return semanticPhysicalInventory{}, authority.Error{Code: authority.InvalidContract}
		}
		parent := name[:max(0, strings.LastIndex(name, "/"))]
		if strings.Contains(name, "/") {
			if _, exists := i.proofs[parent]; !exists {
				return semanticPhysicalInventory{}, authority.Error{Code: authority.InvalidContract}
			}
		}
		next.proofs[name] = p
	}
	return next, nil
}

type semanticPhysicalPass struct {
	expected             semanticPhysicalInventory
	seen                 map[string]semanticPhysicalProof
	usage                semanticUsage
	namespaces           int
	retained             map[string]int
	candidates           int
	quarantined          int
	quarantineNamespaces map[string]bool
	stages               int
	stageBytes           int64
	headStages           map[string]int
	generationBytes      map[string]int64
	candidateBytes       int64
	quarantineBytes      int64
}

func newSemanticPhysicalPass(i semanticPhysicalInventory) *semanticPhysicalPass {
	return &semanticPhysicalPass{expected: i, seen: map[string]semanticPhysicalProof{}, retained: map[string]int{}, quarantineNamespaces: map[string]bool{}, headStages: map[string]int{}, generationBytes: map[string]int64{}}
}

func (p *semanticPhysicalPass) account(name string, role semanticPhysicalRole, size int64) error {
	u, ok := p.usage.add(semanticUsage{size, 1})
	if !ok || !u.within(semanticProjectCapacity) {
		return Error{Capacity}
	}
	p.usage = u
	switch role.kind {
	case "namespace":
		p.namespaces++
		if p.namespaces > 2 {
			return Error{Capacity}
		}
	case "generation":
		if p.expected.proofs[name].Retained {
			p.retained[role.namespace]++
			if p.retained[role.namespace] > 2 {
				return Error{Capacity}
			}
		} else {
			p.candidates++
			if p.candidates > 2 {
				return Error{Capacity}
			}
		}
	case "quarantine_generation":
		p.quarantined++
		p.quarantineNamespaces[p.expected.proofs[name].Namespace] = true
		if p.quarantined > 4 || len(p.quarantineNamespaces) > 2 {
			return Error{Capacity}
		}
	case "control_stage":
		p.stages++
		p.stageBytes += size
		if p.stages > semanticControlStages || p.stageBytes > semanticControlStageBytes {
			return Error{Capacity}
		}
	case "head_stage":
		p.headStages[role.namespace]++
		if p.headStages[role.namespace] > 2 {
			return Error{Capacity}
		}
	}
	if !role.directory && role.generation != "" {
		p.generationBytes[role.generation] += size
		if p.generationBytes[role.generation] > semanticGenerationBytes {
			return Error{Capacity}
		}
		if strings.HasPrefix(role.generation, semanticQuarantineDirectory+"/") {
			p.quarantineBytes += size
			if p.quarantineBytes > semanticQuarantineBytes {
				return Error{Capacity}
			}
		} else if !p.expected.proofs[role.generation].Retained {
			p.candidateBytes += size
			if p.candidateBytes > 64<<20 {
				return Error{Capacity}
			}
		}
	}
	return nil
}

func (p *semanticPhysicalPass) fingerprint() string {
	type record struct {
		Role  string
		Proof semanticPhysicalProof
	}
	names := make([]string, 0, len(p.seen))
	for name := range p.seen {
		names = append(names, name)
	}
	sort.Strings(names)
	records := make([]record, 0, len(names))
	for _, name := range names {
		records = append(records, record{name, p.seen[name]})
	}
	b, _ := json.Marshal(records)
	return hash(b)
}

func semanticPhysicalSource(scope *semanticQuotaScope) error {
	s := scope.store
	if !s.mu.TryLock() {
		return semanticPhysicalBusy{}
	}
	defer s.mu.Unlock()
	if s.closed || s.semanticQuotaOwner != scope.owner || s.lease == nil || s.lease.runtimeGuard == nil || s.fenced || s.logicalFenced {
		return Error{ProjectionCorrupt}
	}
	if bound := (semanticQuotaBinding{s.manifest.ID, s.instance, manifestHash(s.manifest), s.lease.runtimeDirectoryIdentity}); bound != scope.binding {
		return Error{ProjectionCorrupt}
	}
	return s.verifyPhysicalBinding()
}

func fenceSemanticPhysicalFailure(scope *semanticQuotaScope, err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if _, busy := err.(semanticPhysicalBusy); !busy {
		scope.owner.uncertain.Store(true)
	}
}

func scanSemanticPhysical(ctx context.Context, scope *semanticQuotaScope, expected semanticPhysicalInventory) (semanticQuotaImage, error) {
	return scanSemanticPhysicalWithHook(ctx, scope, expected, nil)
}

// The hook is a private deterministic test seam, not config or runtime input.
func scanSemanticPhysicalWithHook(ctx context.Context, scope *semanticQuotaScope, expected semanticPhysicalInventory, between func()) (image semanticQuotaImage, err error) {
	if ctx.Err() != nil {
		return image, ctx.Err()
	}
	if scope == nil || scope.owner == nil || !scope.owner.owns(scope) {
		return image, authority.Error{Code: authority.NoAuthority}
	}
	expected, err = expected.snapshot(scope)
	if err != nil {
		return image, err
	}
	if err = semanticPhysicalSource(scope); err != nil {
		fenceSemanticPhysicalFailure(scope, err)
		return image, err
	}
	lease, err := scope.owner.acquireIO(scope)
	if err != nil {
		return image, err
	}
	var root *os.File
	defer func() {
		if root != nil && root.Close() != nil {
			fenceSemanticPhysicalFailure(scope, Error{ProjectionUncertain})
			if err == nil {
				err = Error{ProjectionUncertain}
			}
		}
		if lease.release() != nil {
			fenceSemanticPhysicalFailure(scope, Error{ProjectionUncertain})
			if err == nil {
				err = Error{ProjectionUncertain}
			}
		}
		if err != nil {
			image = semanticQuotaImage{}
			fenceSemanticPhysicalFailure(scope, err)
		}
	}()
	if err = semanticPhysicalSource(scope); err != nil {
		return image, err
	}
	root, err = openPrivateDirectory(scope.store.directory)
	if err != nil {
		return image, Error{ProjectionCorrupt}
	}
	id, e := physicalIdentity(root)
	if e != nil || id != scope.binding.root {
		return image, Error{ProjectionCorrupt}
	}
	var first string
	for attempt := 0; attempt < 2; attempt++ {
		pass := newSemanticPhysicalPass(expected)
		if err = walkSemanticPhysical(ctx, root, "", pass); err != nil {
			return image, err
		}
		if len(pass.seen) != len(expected.proofs) {
			return image, Error{ProjectionCorrupt}
		}
		fp := pass.fingerprint()
		if attempt == 0 {
			first = fp
			if between != nil {
				between()
			}
		} else if fp != first {
			return image, Error{ProjectionCorrupt}
		}
		image = semanticQuotaImage{scope.binding, pass.usage, fp}
	}
	if err = semanticPhysicalSource(scope); err != nil {
		return semanticQuotaImage{}, err
	}
	return image, nil
}
