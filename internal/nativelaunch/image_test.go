package nativelaunch

import (
	"context"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"

	"gitcode-mcp/internal/authority"
)

func requireImageCode(t *testing.T, err error, code authority.Code) {
	t.Helper()
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatal("native image refusal class missing or unsafe")
	}
}

func syntheticImageToken() [8]uint32 { return [8]uint32{0, 100, 200, 100, 200, 300, 1, 10} }

func syntheticCodeRead(_ [8]uint32, op uint32, out []byte) error {
	if op == darwinStatus {
		binary.NativeEndian.PutUint32(out, darwinRequiredFlags)
		return nil
	}
	for i := 0; i < 20; i++ {
		out[i] = 1
	}
	out[20] = 2
	return nil
}

func TestNativeImageStableAuditedBracket(t *testing.T) {
	token := syntheticImageToken()
	var ops []uint32
	reads := 0
	image, err := sampleDarwinImage(context.Background(), 300, 100, func() ([8]uint32, error) { reads++; return token, nil }, func(t [8]uint32, op uint32, out []byte) error {
		if t != token {
			return authority.Error{Code: authority.InvalidContract}
		}
		ops = append(ops, op)
		return syntheticCodeRead(t, op, out)
	})
	if err != nil || reads != 2 || len(ops) != 4 || ops[0] != 0 || ops[1] != 18 || ops[2] != 18 || ops[3] != 0 || image.token != token || image.hashType != 2 || image.flags != darwinRequiredFlags {
		t.Fatal("stable exact image bracket refused")
	}
}

func TestNativeImageRefusesTokenHashTypeStatusAndIncarnationChanges(t *testing.T) {
	for _, variant := range []string{"pid", "uid", "real-uid", "version", "token-error", "code-error", "unsigned", "invalid", "not-enforced", "debugged", "killed", "invalid-allowed", "unknown-flag", "zero-hash", "sha1", "unknown-type", "hash-changed", "type-changed", "status-changed", "token-changed", "final-token-error"} {
		t.Run(variant, func(t *testing.T) {
			token := syntheticImageToken()
			switch variant {
			case "pid":
				token[5]++
			case "uid":
				token[1]++
			case "real-uid":
				token[3]++
			case "version":
				token[7] = 0
			}
			reads, ops := 0, 0
			_, err := sampleDarwinImage(context.Background(), 300, 100, func() ([8]uint32, error) {
				reads++
				if variant == "token-error" || variant == "final-token-error" && reads == 2 {
					return [8]uint32{}, errors.New("synthetic-private-error")
				}
				if variant == "token-changed" && reads == 2 {
					token[7]++
				}
				return token, nil
			}, func(token [8]uint32, op uint32, out []byte) error {
				ops++
				if variant == "code-error" {
					return errors.New("synthetic-private-error")
				}
				_ = syntheticCodeRead(token, op, out)
				if op == darwinStatus {
					flags := darwinRequiredFlags
					switch variant {
					case "unsigned":
						flags &^= 0x20000000
					case "invalid":
						flags &^= 1
					case "not-enforced":
						flags &^= 0x1000
					case "debugged":
						flags |= 0x10000000
					case "killed":
						flags |= 0x1000000
					case "invalid-allowed":
						flags |= 0x20
					case "unknown-flag":
						flags |= 0x40
					case "status-changed":
						if ops == 4 {
							flags |= 2
						}
					}
					binary.NativeEndian.PutUint32(out, flags)
				} else {
					switch variant {
					case "zero-hash":
						for i := 0; i < 20; i++ {
							out[i] = 0
						}
					case "sha1":
						out[20] = 1
					case "unknown-type":
						out[20] = 255
					case "hash-changed":
						if ops == 3 {
							out[0]++
						}
					case "type-changed":
						if ops == 3 {
							out[20] = 3
						}
					}
				}
				return nil
			})
			code := authority.PolicyDenied
			switch variant {
			case "token-error", "code-error", "final-token-error":
				code = authority.Unavailable
			case "hash-changed", "type-changed", "status-changed", "token-changed":
				code = authority.Conflict
			}
			requireImageCode(t, err, code)
		})
	}
}

func TestNativeImageBoundsAndCancellationBeforeEveryCallback(t *testing.T) {
	read := func() ([8]uint32, error) { t.Error("invalid image reached token reader"); return [8]uint32{}, nil }
	pids := []int{0, -1}
	if strconv.IntSize == 64 {
		large := uint64(1) << 31
		pids = append(pids, int(large))
	}
	for _, pid := range pids {
		_, err := sampleDarwinImage(context.Background(), pid, 100, read, syntheticCodeRead)
		requireImageCode(t, err, authority.PolicyDenied)
	}
	_, err := sampleDarwinImage(context.Background(), 300, -1, read, syntheticCodeRead)
	requireImageCode(t, err, authority.PolicyDenied)
	_, err = sampleDarwinImage(context.Background(), 300, 100, nil, syntheticCodeRead)
	requireImageCode(t, err, authority.PolicyDenied)
	_, err = sampleDarwinImage(context.Background(), 300, 100, read, nil)
	requireImageCode(t, err, authority.PolicyDenied)
	for stop := 0; stop <= 6; stop++ {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		if stop == 0 {
			cancel()
		}
		advance := func() {
			calls++
			if calls == stop {
				cancel()
			}
		}
		_, err := sampleDarwinImage(ctx, 300, 100, func() ([8]uint32, error) { advance(); return syntheticImageToken(), nil }, func(token [8]uint32, op uint32, out []byte) error {
			advance()
			return syntheticCodeRead(token, op, out)
		})
		cancel()
		if !errors.Is(err, context.Canceled) || calls != stop {
			t.Fatal("native image cancellation was hidden or callback continued")
		}
	}
}

func TestNativeImageCancellationDominatesAdapterFailure(t *testing.T) {
	for _, where := range []string{"initial-token", "code", "final-token"} {
		ctx, cancel := context.WithCancel(context.Background())
		reads := 0
		_, err := sampleDarwinImage(ctx, 300, 100, func() ([8]uint32, error) {
			reads++
			if where == "initial-token" || where == "final-token" && reads == 2 {
				cancel()
				return [8]uint32{}, errors.New("synthetic-private-adapter")
			}
			return syntheticImageToken(), nil
		}, func(token [8]uint32, op uint32, out []byte) error {
			if where == "code" {
				cancel()
				return errors.New("synthetic-private-adapter")
			}
			return syntheticCodeRead(token, op, out)
		})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("adapter failure hid native caller cancellation")
		}
	}
}

func TestNativeImageFiniteStrongHashTypes(t *testing.T) {
	for _, kind := range []byte{2, 3, 4} {
		image, err := sampleDarwinImage(context.Background(), 300, 100, func() ([8]uint32, error) { return syntheticImageToken(), nil }, func(token [8]uint32, op uint32, out []byte) error {
			_ = syntheticCodeRead(token, op, out)
			if op == darwinHashInfo {
				out[20] = kind
			}
			return nil
		})
		if err != nil || image.hashType != kind {
			t.Fatal("known strong kernel hash type refused")
		}
	}
}
