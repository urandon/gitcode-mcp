package nativelaunch

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestNativeTranscriptExactIndependentVector(t *testing.T) {
	digest, err := transcriptDigest(1, 1, 0, []byte(`{"synthetic":"example"}`))
	// Independent hashlib/struct big-endian vector, not this encoder's output.
	if err != nil || hex.EncodeToString(digest[:]) != "62844cc86b967d29cc6380f453957ba716e4a1390dad56919aad07863f413cd1" {
		t.Fatal("exact transcript vector differs")
	}
	for _, c := range []struct{ direction, stage, rights uint32 }{{2, 2, 3}, {2, 2, 18}, {1, 3, 0}, {2, 4, 0}} {
		other, err := transcriptDigest(c.direction, c.stage, c.rights, []byte(`{"synthetic":"example"}`))
		if err != nil || other == digest {
			t.Fatal("direction, stage or rights not bound")
		}
	}
	for _, changed := range []string{`{"synthetic":"changed"}`, ` {"synthetic":"example"}`, `{"synthetic":"example"} `} {
		other, err := transcriptDigest(1, 1, 0, []byte(changed))
		if err != nil || other == digest {
			t.Fatal("exact packet bytes not bound")
		}
	}
	minimum, _ := transcriptDigest(2, 2, 3, []byte{1})
	maximum, _ := transcriptDigest(2, 2, 18, []byte{1})
	if minimum == maximum {
		t.Fatal("exact GRANT rights cardinality not bound")
	}
}

func TestNativeTranscriptFiniteShapeAndByteBounds(t *testing.T) {
	for _, c := range []struct{ direction, stage, rights uint32 }{{0, 1, 0}, {2, 1, 0}, {1, 2, 3}, {2, 3, 0}, {1, 4, 0}, {1, 0, 0}, {1, 5, 0}, {1, 1, 1}, {2, 2, 0}, {2, 2, 2}, {2, 2, 19}, {1, 3, 1}, {2, 4, 1}} {
		_, err := transcriptDigest(c.direction, c.stage, c.rights, []byte{1})
		requireImageCode(t, err, authority.InvalidContract)
	}
	for _, size := range []int{0, 1, 64 << 10, (64 << 10) + 1} {
		_, err := transcriptDigest(1, 1, 0, make([]byte, size))
		if size == 0 || size > 64<<10 {
			requireImageCode(t, err, authority.InvalidContract)
		} else if err != nil {
			t.Fatal("bounded packet refused")
		}
	}
}

func syntheticBundleDigest() [32]byte {
	var digest [32]byte
	copy(digest[:], []byte{0, 0, 0, 0, 0x80, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0x7f, 0xff, 0xff, 0xff, 1, 2, 3, 4, 0xaa, 0xbb, 0xcc, 0xdd, 0, 0xff, 0, 0xff, 0xff, 0, 0xff, 0})
	return digest
}

func TestNativeDigestBundleBitPatternsAndEveryStage(t *testing.T) {
	digest := syntheticBundleDigest()
	words := digestWords(digest)
	if words[0] != 0 || words[1] != -2147483648 || words[2] != -1 || words[3] != 2147483647 || uint32(words[4]) != 0x01020304 {
		t.Fatal("unsigned digest bit patterns or endian differ")
	}
	for stage := uint32(1); stage <= 4; stage++ {
		direction := uint32(1 + (stage+1)%2)
		b, err := newDigestBundle(direction, stage, syntheticImageToken(), digest)
		if err != nil {
			t.Fatal("valid private bundle shape refused")
		}
		start := uint32(0)
		if stage > 2 {
			start = 8
		}
		for i, value := range words {
			finish, err := b.consume(context.Background(), digestWord{value: value, sequence: start + uint32(i), token: syntheticImageToken()})
			if err != nil || finish != (i == 7) {
				t.Fatal("exact eight-word bundle did not settle once")
			}
		}
		_, err = b.consume(context.Background(), digestWord{})
		requireImageCode(t, err, authority.Conflict)
		if b.expected != ([32]byte{}) || b.words != ([32]byte{}) || b.token != ([8]uint32{}) {
			t.Fatal("settled private bundle retained protected values")
		}
	}
}

func TestNativeDigestBundleRefusesEverySenderOrderOrPacketSubstitution(t *testing.T) {
	for _, variant := range []string{"sender", "incarnation", "uid", "skip", "duplicate", "old-stage", "word"} {
		for at := 0; at < 8; at++ {
			b, err := newDigestBundle(1, 1, syntheticImageToken(), syntheticBundleDigest())
			if err != nil {
				t.Fatal("synthetic bundle unavailable")
			}
			words := digestWords(syntheticBundleDigest())
			refused := false
			for i, value := range words {
				word := digestWord{value: value, sequence: uint32(i), token: syntheticImageToken()}
				if i == at {
					switch variant {
					case "sender":
						word.token[5]++
					case "incarnation":
						word.token[7]++
					case "uid":
						word.token[1]++
					case "skip":
						word.sequence++
					case "duplicate":
						word.sequence--
					case "old-stage":
						word.sequence += 8
					case "word":
						word.value ^= 1
					}
				}
				finish, err := b.consume(context.Background(), word)
				if err != nil {
					requireImageCode(t, err, authority.PolicyDenied)
					refused = true
					break
				}
				if finish {
					t.Fatal("substituted sender, order or packet settled")
				}
			}
			if !refused || !b.closed {
				t.Fatal("substitution did not permanently refuse")
			}
			_, err = b.consume(context.Background(), digestWord{})
			requireImageCode(t, err, authority.Conflict)
		}
	}
}

func TestNativeDigestBundleCancellationAtEveryWordCannotResync(t *testing.T) {
	for stop := 0; stop < 8; stop++ {
		b, _ := newDigestBundle(1, 1, syntheticImageToken(), syntheticBundleDigest())
		ctx, cancel := context.WithCancel(context.Background())
		for i, value := range digestWords(syntheticBundleDigest()) {
			if i == stop {
				cancel()
			}
			finish, err := b.consume(ctx, digestWord{value: value, sequence: uint32(i), token: syntheticImageToken()})
			if i == stop {
				if finish || !errors.Is(err, context.Canceled) || !b.closed {
					t.Fatal("cancelled partial bundle continued")
				}
				break
			}
			if finish || err != nil {
				t.Fatal("partial bundle settled before eight words")
			}
		}
		cancel()
		_, err := b.consume(context.Background(), digestWord{})
		requireImageCode(t, err, authority.Conflict)
	}
}

func TestNativeDigestBundleInvalidInputsAreFinite(t *testing.T) {
	for _, variant := range []string{"direction", "stage", "pid", "version", "unequal-uid"} {
		token, direction, stage := syntheticImageToken(), uint32(1), uint32(1)
		switch variant {
		case "direction":
			direction = 2
		case "stage":
			stage = 0
		case "pid":
			token[5] = 0
		case "version":
			token[7] = 0
		case "unequal-uid":
			token[1]++
		}
		_, err := newDigestBundle(direction, stage, token, syntheticBundleDigest())
		requireImageCode(t, err, authority.InvalidContract)
	}
	var missing *digestBundle
	_, err := missing.consume(context.Background(), digestWord{})
	requireImageCode(t, err, authority.InvalidContract)
	var zero digestBundle
	_, err = zero.consume(context.Background(), digestWord{})
	requireImageCode(t, err, authority.InvalidContract)
	if !zero.closed {
		t.Fatal("zero bundle did not permanently refuse")
	}
	b, _ := newDigestBundle(1, 1, syntheticImageToken(), syntheticBundleDigest())
	_, err = b.consume(nil, digestWord{})
	requireImageCode(t, err, authority.InvalidContract)
	if !b.closed {
		t.Fatal("missing context left bundle reusable")
	}
}
