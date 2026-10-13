package nativelaunch

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"

	"gitcode-mcp/internal/authority"
)

// Private r10 measurement primitives only. A supplied digest or token is not
// native provenance. No caller/transport/grant/adopter is enabled by this file.
const transcriptDomain = "gitcode-mcp/native-r10\x00"

func transcriptShape(direction, stage, rights uint32) bool {
	if stage < 1 || stage > 4 || direction != 1+(stage+1)%2 {
		return false
	}
	if stage == 2 {
		return rights >= 3 && rights <= 18
	}
	return rights == 0
}

// Hashes the exact once-serialized protected packet, not a reconstructed JSON
// map. The future caller must independently validate its schema and actual FD
// role/physical bindings. This is an unkeyed binding digest, not authentication.
func transcriptDigest(direction, stage, rights uint32, payload []byte) ([32]byte, error) {
	if !transcriptShape(direction, stage, rights) || len(payload) == 0 || len(payload) > 64<<10 {
		return [32]byte{}, authority.Error{Code: authority.InvalidContract}
	}
	var fields [20]byte
	for i, value := range [5]uint32{1, direction, stage, uint32(len(payload)), rights} {
		binary.BigEndian.PutUint32(fields[i*4:], value)
	}
	h := sha256.New()
	_, _ = h.Write([]byte(transcriptDomain))
	_, _ = h.Write(fields[:])
	_, _ = h.Write(payload)
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}

func digestWords(digest [32]byte) [8]int32 {
	var words [8]int32
	for i := range words {
		words[i] = int32(binary.BigEndian.Uint32(digest[i*4:]))
	}
	return words
}

// The native adapter must first establish exact successful header shape and
// kernel provenance/current image. This private value cannot establish either;
// unit tests intentionally supply synthetic values without claiming authority.
type digestWord struct {
	value    int32
	sequence uint32
	token    [8]uint32
}

type digestBundle struct {
	expected [32]byte
	token    [8]uint32
	words    [32]byte
	start    uint32
	count    uint32
	prepared bool
	closed   bool
}

func newDigestBundle(direction, stage uint32, token [8]uint32, expected [32]byte) (*digestBundle, error) {
	// Rights count is relevant to the packet digest, not this word assembler.
	rights := uint32(0)
	if stage == 2 {
		rights = 3
	}
	if !transcriptShape(direction, stage, rights) || token[5] == 0 || token[5] > (1<<31)-1 || token[7] == 0 || token[1] != token[3] {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	start := uint32(0)
	if stage > 2 {
		start = 8
	}
	return &digestBundle{expected: expected, token: token, start: start, prepared: true}, nil
}

func (b *digestBundle) stop() {
	b.expected, b.words, b.token = [32]byte{}, [32]byte{}, [8]uint32{}
	b.closed = true
}

// finish=false is a pending observation only. Any refusal/cancellation closes
// this one-shot assembler; no resync, reset or truncated-digest success exists.
func (b *digestBundle) consume(ctx context.Context, word digestWord) (finish bool, err error) {
	if b == nil || ctx == nil {
		if b != nil {
			b.stop()
		}
		return false, authority.Error{Code: authority.InvalidContract}
	}
	if b.closed {
		return false, authority.Error{Code: authority.Conflict}
	}
	if !b.prepared {
		b.stop()
		return false, authority.Error{Code: authority.InvalidContract}
	}
	if err := ctx.Err(); err != nil {
		b.stop()
		return false, err
	}
	if b.count >= 8 || word.token != b.token || word.sequence != b.start+b.count {
		b.stop()
		return false, authority.Error{Code: authority.PolicyDenied}
	}
	binary.BigEndian.PutUint32(b.words[b.count*4:], uint32(word.value))
	b.count++
	if b.count < 8 {
		return false, nil
	}
	matched := subtle.ConstantTimeCompare(b.expected[:], b.words[:]) == 1
	b.stop()
	if !matched {
		return false, authority.Error{Code: authority.PolicyDenied}
	}
	return true, nil
}
