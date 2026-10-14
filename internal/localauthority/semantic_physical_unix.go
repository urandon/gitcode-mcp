//go:build darwin || linux

package localauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openSemanticPhysicalNode(parent *os.File, name string, directory bool) (*os.File, int64, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags, 0)
	if err != nil {
		return nil, 0, Error{ProjectionCorrupt}
	}
	f := os.NewFile(uintptr(fd), "semantic-owned-role")
	var stat unix.Stat_t
	want := uint32(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if unix.Fstat(fd, &stat) != nil || uint32(stat.Mode)&unix.S_IFMT != want || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || !directory && stat.Nlink != 1 || stat.Size < 0 {
		_ = f.Close()
		return nil, 0, Error{ProjectionCorrupt}
	}
	if directory {
		return f, 0, nil
	}
	return f, stat.Size, nil
}

func digestSemanticPhysical(ctx context.Context, file *os.File, size, cap int64) (string, error) {
	if size < 0 || size > cap {
		return "", Error{Capacity}
	}
	h := sha256.New()
	buf := make([]byte, 32<<10)
	var total int64
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, err := file.Read(buf)
		if n > 0 {
			if int64(n) > size-total {
				return "", Error{ProjectionCorrupt}
			}
			total += int64(n)
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", Error{ProjectionCorrupt}
		}
	}
	if total != size {
		return "", Error{ProjectionCorrupt}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func walkSemanticPhysical(ctx context.Context, directory *os.File, prefix string, pass *semanticPhysicalPass) (result error) {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Error{ProjectionCorrupt}
	}
	scan := os.NewFile(uintptr(fd), "semantic-role-enumeration")
	defer func() {
		if scan.Close() != nil && result == nil {
			result = Error{ProjectionCorrupt}
		}
	}()
	rootCount := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		names, e := scan.Readdirnames(32)
		for _, name := range names {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if prefix == "" {
				rootCount++
				if rootCount > semanticRootEntries {
					return Error{Capacity}
				}
				if !strings.HasPrefix(name, "runtime-semantic-") {
					continue
				}
			}
			if pass.usage.entries >= semanticProjectEntries {
				return Error{Capacity}
			}
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}
			role, valid := semanticRole(path)
			proof, owned := pass.expected.proofs[path]
			if !valid || !owned {
				return Error{ProjectionCorrupt}
			}
			if _, duplicate := pass.seen[path]; duplicate {
				return Error{ProjectionCorrupt}
			}
			f, size, err := openSemanticPhysicalNode(directory, name, role.directory)
			if err != nil {
				return err
			}
			err = visitSemanticPhysical(ctx, f, path, role, proof, size, pass)
			closed := f.Close()
			if err != nil {
				return err
			}
			if closed != nil {
				return Error{ProjectionCorrupt}
			}
		}
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return Error{ProjectionCorrupt}
		}
	}
}

func visitSemanticPhysical(ctx context.Context, f *os.File, path string, role semanticPhysicalRole, proof semanticPhysicalProof, size int64, pass *semanticPhysicalPass) error {
	if err := pass.account(path, role, size); err != nil {
		return err
	}
	if !role.directory && size > role.cap {
		return Error{Capacity}
	}
	id, err := physicalIdentity(f)
	if err != nil || id != proof.Identity || size != proof.Bytes {
		return Error{ProjectionCorrupt}
	}
	pass.seen[path] = proof
	if role.directory {
		return walkSemanticPhysical(ctx, f, path, pass)
	}
	digest, err := digestSemanticPhysical(ctx, f, size, role.cap)
	if err != nil {
		return err
	}
	if digest != proof.Digest {
		return Error{ProjectionCorrupt}
	}
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Size != size {
		return Error{ProjectionCorrupt}
	}
	return nil
}
