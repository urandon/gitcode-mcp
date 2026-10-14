//go:build darwin || linux

package localauthority

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func semanticPhysicalFixture(t *testing.T) (*semanticQuota, *semanticQuotaScope, semanticPhysicalInventory) {
	t.Helper()
	s := semanticPreparedStore(t)
	createIssue(t, s, "physical-scanner-source")
	q, err := newSemanticQuota(context.Background(), []*Store{s})
	if err != nil {
		t.Fatal("physical quota fixture unavailable")
	}
	scope, err := q.scopeFor(s)
	if err != nil {
		t.Fatal("physical scope unavailable")
	}
	return q, scope, semanticPhysicalInventory{scope.binding, map[string]semanticPhysicalProof{}}
}

func semanticPhysicalNodeFixture(t *testing.T, scope *semanticQuotaScope, inventory *semanticPhysicalInventory, role string, body []byte) {
	t.Helper()
	r, ok := semanticRole(role)
	if !ok {
		t.Fatal("invalid synthetic role")
	}
	path := filepath.Join(scope.store.directory, role)
	if r.directory {
		if os.Mkdir(path, 0700) != nil {
			t.Fatal("synthetic role directory unavailable")
		}
	} else if os.WriteFile(path, body, 0600) != nil {
		t.Fatal("synthetic role leaf unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("synthetic role descriptor unavailable")
	}
	id, err := physicalIdentity(f)
	if f.Close() != nil || err != nil {
		t.Fatal("synthetic physical proof unavailable")
	}
	p := semanticPhysicalProof{Identity: id}
	if !r.directory {
		p.Bytes = int64(len(body))
		p.Digest = hash(body)
	}
	if r.kind == "quarantine_generation" {
		p.Namespace = hash([]byte("synthetic-quarantine-namespace"))
	}
	inventory.proofs[role] = p
}

func semanticPhysicalGenerationFixture(t *testing.T, scope *semanticQuotaScope, i *semanticPhysicalInventory, ns string, build authority.UUID, retained bool) string {
	t.Helper()
	if _, ok := i.proofs[semanticVectorDirectory]; !ok {
		semanticPhysicalNodeFixture(t, scope, i, semanticVectorDirectory, nil)
	}
	n := semanticVectorDirectory + "/ns-" + ns
	if _, ok := i.proofs[n]; !ok {
		semanticPhysicalNodeFixture(t, scope, i, n, nil)
	}
	g := n + "/gen-" + string(build)
	semanticPhysicalNodeFixture(t, scope, i, g, nil)
	p := i.proofs[g]
	p.Retained = retained
	i.proofs[g] = p
	return g
}

func TestSemanticPhysicalKnownInventoryCountsAndPreservesSource(t *testing.T) {
	q, s, i := semanticPhysicalFixture(t)
	before := semanticSnapshot(t, s.store, authority.Issue)
	// Canonical roles may contain opaque links/unknown children: they are never
	// entered by inventory scanning (the fixed source inode guard remains).
	if os.Symlink("not-present", filepath.Join(s.store.directory, "synthetic-canonical-opaque")) != nil {
		t.Fatal("opaque canonical fixture unavailable")
	}
	image, err := scanSemanticPhysical(context.Background(), s, i)
	if err != nil || image.usage != (semanticUsage{}) || !image.valid() {
		t.Fatal("empty measurement failed or invented enrollment")
	}
	semanticPhysicalNodeFixture(t, s, &i, semanticBirthFile, []byte("synthetic-anchor"))
	semanticPhysicalNodeFixture(t, s, &i, semanticEnrollmentFile, []byte("synthetic-enrollment"))
	semanticPhysicalNodeFixture(t, s, &i, semanticControlFile, []byte("synthetic-control"))
	semanticPhysicalNodeFixture(t, s, &i, "runtime-semantic-stage-"+string(semanticTestBuild)+".json", []byte("partial-stage"))
	g := semanticPhysicalGenerationFixture(t, s, &i, hash([]byte("synthetic-namespace")), semanticTestBuild, true)
	semanticPhysicalNodeFixture(t, s, &i, g+"/manifest.json", []byte("not-a-publication-proof"))
	semanticPhysicalNodeFixture(t, s, &i, g+"/shard-00.bin", nil)
	semanticPhysicalNodeFixture(t, s, &i, semanticQuarantineDirectory, nil)
	retired := semanticQuarantineDirectory + "/retired-" + string(testProject)
	semanticPhysicalNodeFixture(t, s, &i, retired, nil)
	semanticPhysicalNodeFixture(t, s, &i, retired+"/manifest.json", []byte("retired-metadata"))
	var bytes int64
	for _, p := range i.proofs {
		bytes += p.Bytes
	}
	image, err = scanSemanticPhysical(context.Background(), s, i)
	if err != nil || image.usage != (semanticUsage{bytes, len(i.proofs)}) || !image.valid() || q.uncertain.Load() {
		t.Fatal("known group image did not conserve physical bytes/entries")
	}
	if q.observe(s, image) != nil {
		t.Fatal("qualified physical image could not supply quota")
	}
	if !reflect.DeepEqual(before, semanticSnapshot(t, s.store, authority.Issue)) {
		t.Fatal("physical inventory changed canonical source/history")
	}
	if q.ioActive.Load() != 0 {
		t.Fatal("scanner retained an unjoined descriptor lease")
	}
}

func TestSemanticPhysicalUnsafeInventoryRefusesWithoutSourceDamage(t *testing.T) {
	for _, kind := range []string{"missing", "symlink", "hardlink", "public-mode", "directory-as-leaf", "same-size-content", "unknown-role", "unknown-nested", "oversized-sparse", "namespace-symlink"} {
		t.Run(kind, func(t *testing.T) {
			q, s, i := semanticPhysicalFixture(t)
			before := semanticSnapshot(t, s.store, authority.Issue)
			semanticPhysicalNodeFixture(t, s, &i, semanticControlFile, []byte("aa"))
			file := filepath.Join(s.store.directory, semanticControlFile)
			switch kind {
			case "missing":
				if os.Rename(file, filepath.Join(s.store.directory, "saved-control")) != nil {
					t.Fatal("missing control unavailable")
				}
			case "symlink":
				if os.Rename(file, filepath.Join(s.store.directory, "saved-control")) != nil || os.Symlink("saved-control", file) != nil {
					t.Fatal("symlink control unavailable")
				}
			case "hardlink":
				if os.Link(file, filepath.Join(s.store.directory, "saved-control")) != nil {
					t.Fatal("hardlink control unavailable")
				}
			case "public-mode":
				if os.Chmod(file, 0644) != nil {
					t.Fatal("mode control unavailable")
				}
			case "directory-as-leaf":
				if os.Rename(file, filepath.Join(s.store.directory, "saved-control")) != nil || os.Mkdir(file, 0700) != nil {
					t.Fatal("type control unavailable")
				}
			case "same-size-content":
				if os.WriteFile(file, []byte("bb"), 0600) != nil {
					t.Fatal("content control unavailable")
				}
			case "unknown-role":
				if os.WriteFile(filepath.Join(s.store.directory, "runtime-semantic-unknown"), []byte("unowned"), 0600) != nil {
					t.Fatal("unknown control unavailable")
				}
			case "unknown-nested":
				semanticPhysicalNodeFixture(t, s, &i, semanticVectorDirectory, nil)
				if os.WriteFile(filepath.Join(s.store.directory, semanticVectorDirectory, "unknown"), nil, 0600) != nil {
					t.Fatal("nested control unavailable")
				}
			case "oversized-sparse":
				if os.Truncate(file, semanticControlBytes+1) != nil {
					t.Fatal("sparse control unavailable")
				}
			case "namespace-symlink":
				semanticPhysicalNodeFixture(t, s, &i, semanticVectorDirectory, nil)
				dir := filepath.Join(s.store.directory, semanticVectorDirectory)
				if os.Rename(dir, filepath.Join(s.store.directory, "saved-vectors")) != nil || os.Symlink("saved-vectors", dir) != nil {
					t.Fatal("directory control unavailable")
				}
			}
			image, err := scanSemanticPhysical(context.Background(), s, i)
			if err == nil || image.valid() || !q.uncertain.Load() || q.ioActive.Load() != 0 {
				t.Fatal("unsafe/partial image qualified or escaped joined fence")
			}
			if !reflect.DeepEqual(before, semanticSnapshot(t, s.store, authority.Issue)) {
				t.Fatal("unsafe derived inventory damaged source authority")
			}
			if _, err = q.reserve(s, semanticUsage{}); err == nil {
				t.Fatal("physical fence granted fresh growth")
			}
		})
	}
}

func TestSemanticPhysicalChangesBetweenPassesRefuse(t *testing.T) {
	for _, kind := range []string{"content", "inode", "directory", "root", "added-role", "removed-role", "cancel", "source-busy"} {
		t.Run(kind, func(t *testing.T) {
			q, s, i := semanticPhysicalFixture(t)
			semanticPhysicalNodeFixture(t, s, &i, semanticControlFile, []byte("aa"))
			semanticPhysicalNodeFixture(t, s, &i, semanticVectorDirectory, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			locked := false
			image, err := scanSemanticPhysicalWithHook(ctx, s, i, func() {
				file := filepath.Join(s.store.directory, semanticControlFile)
				switch kind {
				case "content":
					if os.WriteFile(file, []byte("bb"), 0600) != nil {
						t.Fatal("content replacement unavailable")
					}
				case "inode":
					if os.Rename(file, filepath.Join(s.store.directory, "saved-control")) != nil || os.WriteFile(file, []byte("aa"), 0600) != nil {
						t.Fatal("inode replacement unavailable")
					}
				case "directory":
					dir := filepath.Join(s.store.directory, semanticVectorDirectory)
					if os.Rename(dir, filepath.Join(s.store.directory, "saved-vectors")) != nil || os.Mkdir(dir, 0700) != nil {
						t.Fatal("directory replacement unavailable")
					}
				case "root":
					if os.Rename(s.store.directory, s.store.directory+".saved") != nil || os.Mkdir(s.store.directory, 0700) != nil {
						t.Fatal("root replacement unavailable")
					}
				case "added-role":
					if os.WriteFile(filepath.Join(s.store.directory, "runtime-semantic-unknown"), nil, 0600) != nil {
						t.Fatal("added role unavailable")
					}
				case "removed-role":
					if os.Rename(file, filepath.Join(s.store.directory, "saved-control")) != nil {
						t.Fatal("removed role unavailable")
					}
				case "cancel":
					cancel()
				case "source-busy":
					s.store.mu.Lock()
					locked = true
				}
			})
			if locked {
				s.store.mu.Unlock()
			}
			if err == nil || image.valid() || q.ioActive.Load() != 0 {
				t.Fatal("changing/interrupted image qualified or leaked I/O")
			}
			if kind == "cancel" {
				if !errors.Is(err, context.Canceled) || q.uncertain.Load() {
					t.Fatal("cancelled pure measurement became corruption or image")
				}
			} else if kind == "source-busy" {
				if _, ok := err.(semanticPhysicalBusy); !ok || q.uncertain.Load() {
					t.Fatal("source mutex contention became physical corruption")
				}
			} else if !q.uncertain.Load() {
				t.Fatal("physical change lost its admission fence")
			}
		})
	}
}

func TestSemanticPhysicalFreezeExpectationAndJoinBeforeWriterRelease(t *testing.T) {
	s, p := semanticPreparedWriterFixture(t, testProject)
	q, err := newSemanticQuota(context.Background(), []*Store{s})
	if err != nil {
		t.Fatal("quota fixture failed")
	}
	scope, _ := q.scopeFor(s)
	i := semanticPhysicalInventory{scope.binding, map[string]semanticPhysicalProof{}}
	semanticPhysicalNodeFixture(t, scope, &i, semanticControlFile, []byte("owned"))
	image, err := scanSemanticPhysicalWithHook(context.Background(), scope, i, func() {
		delete(i.proofs, semanticControlFile)
		if q.ioActive.Load() != 1 || s.Close() == nil {
			t.Error("scan did not hold registered joined writer ownership")
		}
		if other, e := p.OpenWriter(context.Background(), s.directory, testProject); e == nil {
			t.Error("unjoined scan admitted another writer")
			_ = other.Close()
		}
	})
	if err != nil || !image.valid() || image.usage.entries != 1 || q.ioActive.Load() != 0 {
		t.Fatal("frozen expectation or joined scan failed")
	}
	if q.observe(scope, image) == nil {
		t.Fatal("closing quota admitted a final image as fresh growth")
	}
	if s.Close() != nil {
		t.Fatal("scan joined but source close remained blocked")
	}
}

func TestSemanticPhysicalProductionComponentCapacity(t *testing.T) {
	for _, kind := range []string{"root-enumeration", "control-stages", "namespaces", "retained-generations", "candidates", "quarantine-containers", "quarantine-namespaces"} {
		t.Run(kind, func(t *testing.T) {
			q, s, i := semanticPhysicalFixture(t)
			switch kind {
			case "root-enumeration":
				for n := 0; n <= semanticRootEntries; n++ {
					if os.WriteFile(filepath.Join(s.store.directory, "opaque-"+strconv.Itoa(n)), nil, 0600) != nil {
						t.Fatal("root capacity fixture failed")
					}
				}
			case "control-stages":
				for n := 0; n <= semanticControlStages; n++ {
					id, e := authority.NewUUID()
					if e != nil {
						t.Fatal("synthetic stage identity failed")
					}
					semanticPhysicalNodeFixture(t, s, &i, "runtime-semantic-stage-"+string(id)+".json", nil)
				}
			case "namespaces":
				semanticPhysicalNodeFixture(t, s, &i, semanticVectorDirectory, nil)
				for n := 0; n < 3; n++ {
					semanticPhysicalNodeFixture(t, s, &i, semanticVectorDirectory+"/ns-"+hash([]byte(strconv.Itoa(n))), nil)
				}
			case "retained-generations", "candidates":
				for n := 0; n < 3; n++ {
					id, e := authority.NewUUID()
					if e != nil {
						t.Fatal("synthetic generation identity failed")
					}
					semanticPhysicalGenerationFixture(t, s, &i, hash([]byte("synthetic-namespace")), id, kind == "retained-generations")
				}
			case "quarantine-containers", "quarantine-namespaces":
				semanticPhysicalNodeFixture(t, s, &i, semanticQuarantineDirectory, nil)
				count := 5
				if kind == "quarantine-namespaces" {
					count = 3
				}
				for n := 0; n < count; n++ {
					id, e := authority.NewUUID()
					if e != nil {
						t.Fatal("synthetic retirement identity failed")
					}
					role := semanticQuarantineDirectory + "/retired-" + string(id)
					semanticPhysicalNodeFixture(t, s, &i, role, nil)
					if kind == "quarantine-namespaces" {
						proof := i.proofs[role]
						proof.Namespace = hash([]byte(strconv.Itoa(n)))
						i.proofs[role] = proof
					}
				}
			}
			image, err := scanSemanticPhysical(context.Background(), s, i)
			var typed Error
			if !errors.As(err, &typed) || typed.Code != Capacity || image.valid() || !q.uncertain.Load() {
				t.Fatal("production physical capacity branch not enforced")
			}
		})
	}
}

func TestSemanticPhysicalRejectsUnboundExpectationBeforeIO(t *testing.T) {
	q, s, i := semanticPhysicalFixture(t)
	for _, role := range []string{"/absolute", "../escape", semanticVectorDirectory + "/ns-" + hash([]byte("scope")) + "/gen-" + string(semanticTestBuild) + "/extra/manifest.json", "runtime-semantic-stage-not-uuid.json"} {
		bad := semanticPhysicalInventory{i.binding, map[string]semanticPhysicalProof{role: {Identity: hash([]byte("asserted")), Digest: hash(nil)}}}
		if image, err := scanSemanticPhysical(context.Background(), s, bad); err == nil || image.valid() || q.uncertain.Load() || q.ioActive.Load() != 0 {
			t.Fatal("invalid selector/ownership assertion performed inspection")
		}
	}
	i.binding.root = hash([]byte("foreign-root"))
	if _, err := scanSemanticPhysical(context.Background(), s, i); err == nil || q.uncertain.Load() {
		t.Fatal("foreign source binding admitted")
	}
}

func TestSemanticPhysicalFIFORefusalInBoundedChild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("bounded child executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSemanticPhysicalFIFOChild$")
	cmd.Env = []string{"GCM_SEMANTIC_FIFO_CHILD=1"}
	if cmd.Run() != nil || ctx.Err() != nil {
		t.Fatal("FIFO gate blocked or failed in bounded actual child")
	}
}

func TestSemanticPhysicalFIFOChild(t *testing.T) {
	if os.Getenv("GCM_SEMANTIC_FIFO_CHILD") == "" {
		return
	}
	q, s, i := semanticPhysicalFixture(t)
	semanticPhysicalNodeFixture(t, s, &i, semanticControlFile, []byte("owned"))
	path := filepath.Join(s.store.directory, semanticControlFile)
	if os.Rename(path, filepath.Join(s.store.directory, "saved-control")) != nil || unix.Mkfifo(path, 0600) != nil {
		t.Fatal("FIFO fixture unavailable")
	}
	image, err := scanSemanticPhysical(context.Background(), s, i)
	if err == nil || image.valid() || !q.uncertain.Load() || q.ioActive.Load() != 0 {
		t.Fatal("FIFO became a qualified image")
	}
}

func TestSemanticPhysicalRoleGrammar(t *testing.T) {
	if _, ok := semanticRole(semanticVectorDirectory + "/ns-" + hash(nil) + "/gen-" + string(semanticTestBuild) + "/shard-+1.bin"); ok {
		t.Fatal("noncanonical shard selector accepted")
	}
	for _, bad := range []string{strings.Repeat("x", 129), semanticVectorDirectory + "//head.json", semanticVectorDirectory + "/ns-" + hash(nil) + "/gen-" + string(semanticTestBuild) + "/shard-16.bin"} {
		if _, ok := semanticRole(bad); ok {
			t.Fatal("unbounded/noncanonical role accepted")
		}
	}
}
