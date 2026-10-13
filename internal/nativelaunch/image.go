// Package nativelaunch contains staged native launch qualification primitives.
// These private measurements grant no authority, choose no registry, and are
// not used to enable a native runtime until the complete launch gate qualifies.
package nativelaunch

import (
	"context"
	"encoding/binary"

	"gitcode-mcp/internal/authority"
)

const (
	darwinStatus               = 0
	darwinHashInfo             = 18
	darwinKnownFlags    uint32 = 0xfff3ff3f
	darwinRequiredFlags uint32 = 0x20001001 // signed, enforced, valid
	darwinRefusedFlags  uint32 = 0x11000020 // debugged, killed, invalid allowed
)

// All fields are private and intentionally have no serializable/public DTO.
// An observation is not a kernel credential for a particular received message.
type darwinImage struct {
	token    [8]uint32
	hash     [20]byte
	hashType byte
	flags    uint32
}

type darwinTokenReader func() ([8]uint32, error)
type darwinCodeReader func([8]uint32, uint32, []byte) error

func strongDarwinHash(kind byte) bool { return kind == 2 || kind == 3 || kind == 4 }

// sampleDarwinImage uses one incarnation for every read-only kernel call and
// brackets code identity with status and token observations. Callbacks are
// private adapters/test seams, never selectable launch factories or IPC input.
func sampleDarwinImage(ctx context.Context, pid, uid int, tokenRead darwinTokenReader, codeRead darwinCodeReader) (darwinImage, error) {
	bad := authority.Error{Code: authority.PolicyDenied}
	if ctx.Err() != nil {
		return darwinImage{}, ctx.Err()
	}
	if pid <= 0 || uint64(pid) > (1<<31)-1 || uid < 0 || uint64(uid) > (1<<32)-1 || tokenRead == nil || codeRead == nil {
		return darwinImage{}, bad
	}
	token, err := tokenRead()
	if ctx.Err() != nil {
		return darwinImage{}, ctx.Err()
	}
	if err != nil {
		return darwinImage{}, authority.Error{Code: authority.Unavailable}
	}
	if token[5] != uint32(pid) || token[7] == 0 || token[1] != uint32(uid) || token[3] != uint32(uid) {
		return darwinImage{}, bad
	}
	read := func(op uint32, out []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := codeRead(token, op, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return authority.Error{Code: authority.Unavailable}
		}
		return nil
	}
	var before, after [4]byte
	var first, second [21]byte // csops_cdhash_t: 20-byte hash then one type byte.
	if err := read(darwinStatus, before[:]); err != nil {
		return darwinImage{}, err
	}
	flags := binary.NativeEndian.Uint32(before[:])
	if flags&darwinKnownFlags != flags || flags&darwinRequiredFlags != darwinRequiredFlags || flags&darwinRefusedFlags != 0 {
		return darwinImage{}, bad
	}
	if err := read(darwinHashInfo, first[:]); err != nil {
		return darwinImage{}, err
	}
	if !strongDarwinHash(first[20]) || first == [21]byte{} {
		return darwinImage{}, bad
	}
	var hash [20]byte
	copy(hash[:], first[:20])
	if hash == [20]byte{} {
		return darwinImage{}, bad
	}
	if err := read(darwinHashInfo, second[:]); err != nil {
		return darwinImage{}, err
	}
	if err := read(darwinStatus, after[:]); err != nil {
		return darwinImage{}, err
	}
	if first != second || before != after {
		return darwinImage{}, authority.Error{Code: authority.Conflict}
	}
	if ctx.Err() != nil {
		return darwinImage{}, ctx.Err()
	}
	final, err := tokenRead()
	if ctx.Err() != nil {
		return darwinImage{}, ctx.Err()
	}
	if err != nil {
		return darwinImage{}, authority.Error{Code: authority.Unavailable}
	}
	if final != token {
		return darwinImage{}, authority.Error{Code: authority.Conflict}
	}
	return darwinImage{token: token, hash: hash, hashType: first[20], flags: flags}, nil
}

// A stable sampled image alone does not prove sender association, closed route,
// exec-time isolation, enrollment provenance or an authority grant.
