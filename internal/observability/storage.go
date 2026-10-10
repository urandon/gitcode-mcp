package observability

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

type diskState struct {
	Files         [8]segmentState `json:"files"`
	LegacyCursors [2]LegacyCursor `json:"legacy_cursors"`
	ObservedBoots uint64          `json:"observed_boots"`
	CursorKey     string          `json:"cursor_key"`
	Schema        int             `json:"schema"`
	Revision      uint64          `json:"revision"`
	Generation    string          `json:"generation"`
	Sequence      uint64          `json:"sequence"`
	Clean         bool            `json:"clean"`
	Dropped       uint64          `json:"dropped"`
	LossEpoch     uint64          `json:"loss_epoch"`
	Partial       bool            `json:"partial"`
}
type segmentState struct {
	Present bool   `json:"present"`
	Bytes   int    `json:"bytes"`
	Digest  string `json:"digest,omitempty"`
}
type storage struct {
	reserved                     uint64
	appendFault                  func() error // private deterministic fault seam
	dir, lock                    *os.File
	state                        diskState
	ledgerSlot, outSlot, errSlot int
	lengths                      map[string]int
	last                         [4]uint64
	stateSlot                    int
	newLoss                      bool
}

func decodeStrict(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var tail any
	if err := d.Decode(&tail); err != io.EOF {
		return errors.New("unexpected observation tail")
	}
	return nil
}
func ledgerName(i int) string                { return fmt.Sprintf("ledger-%d.jsonl", i) }
func streamName(stream string, i int) string { return fmt.Sprintf("%s-%d.jsonl", stream, i) }
func dataNames() [8]string {
	return [8]string{ledgerName(0), ledgerName(1), ledgerName(2), ledgerName(3), streamName("stdout", 0), streamName("stdout", 1), streamName("stderr", 0), streamName("stderr", 1)}
}
func segmentInfo(b []byte) segmentState {
	h := sha256.Sum256(b)
	return segmentState{Present: true, Bytes: len(b), Digest: hex.EncodeToString(h[:])}
}
func openStorage(path string) (*storage, []Event, bool, error) {
	dir, err := openDirectory(path, true)
	if err != nil {
		return nil, nil, false, err
	}
	s := &storage{dir: dir, lengths: map[string]int{}}
	s.lock, err = lockDirectory(dir)
	if err != nil {
		dir.Close()
		return nil, nil, false, err
	}
	ok := false
	defer func() {
		if !ok {
			s.close()
		}
	}()
	// A fixed inventory prevents accidental retention of hidden old outputs.
	names, err := dir.Readdirnames(12)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, nil, false, err
	}
	if len(names) > 11 {
		return nil, nil, false, errors.New("unexpected observation inventory")
	}
	allowed := map[string]bool{"owner.lock": true, "state-0.json": true, "state-1.json": true}
	for i := 0; i < 4; i++ {
		allowed[ledgerName(i)] = true
	}
	for _, stream := range []string{"stdout", "stderr"} {
		for i := 0; i < 2; i++ {
			allowed[streamName(stream, i)] = true
		}
	}
	for _, name := range names {
		if !allowed[name] {
			return nil, nil, false, errors.New("unexpected observation inventory")
		}
	}
	metadataSeen, metadataValid := false, false
	for i := 0; i < 2; i++ {
		b, err := readPrivate(dir, fmt.Sprintf("state-%d.json", i), 4096)
		if missing(err) {
			continue
		}
		metadataSeen = true
		if err != nil {
			return nil, nil, false, errors.New("unsafe observation metadata")
		}
		var st diskState
		if err != nil || decodeStrict(b, &st) != nil || st.Schema != 1 || st.Revision == 0 || !hexID.MatchString(st.Generation) || !hexID.MatchString(st.CursorKey) {
			s.state.Partial = true
			s.newLoss = true
			continue
		}
		if !metadataValid || st.Revision > s.state.Revision {
			partial := s.state.Partial
			s.state = st
			s.stateSlot = i
			s.state.Partial = s.state.Partial || partial
			metadataValid = true
		}
	}
	if metadataSeen && !metadataValid {
		s.state.Partial = true
	}
	unclean := metadataValid && !s.state.Clean
	var events []Event
	var observedSequence uint64
	maxima := map[string]uint64{}
	for index, name := range dataNames() {
		b, err := readPrivate(dir, name, SegmentBytes)
		if missing(err) {
			if s.state.Clean && s.state.Files[index].Present {
				s.newLoss = true
			}
			continue
		}
		if err != nil {
			return nil, nil, false, err
		}
		if s.state.Clean && segmentInfo(b) != s.state.Files[index] {
			s.newLoss = true
		}
		physical := len(b)
		validBytes := 0
		var max uint64
		for len(b) > 0 {
			end := bytes.IndexByte(b, '\n')
			if end < 0 || end+1 > MaxEventBytes {
				s.state.Partial = true
				s.newLoss = true
				break
			}
			var e Event
			if decodeStrict(b[:end], &e) != nil || !e.valid() {
				s.state.Partial = true
				s.newLoss = true
				break
			}
			if e.Sequence > max {
				max = e.Sequence
			}
			if e.Sequence > observedSequence {
				observedSequence = e.Sequence
			}
			if len(name) >= 7 && name[:7] == "ledger-" {
				events = append(events, e)
			}
			b = b[end+1:]
			validBytes += end + 1
		}
		// Never append behind an unreadable suffix. The validated prefix stays
		// durable; loss is fenced before the next boot/event is published.
		if validBytes != physical {
			if err := trimPrivate(dir, name, validBytes); err != nil {
				return nil, nil, false, err
			}
		}
		s.lengths[name] = validBytes
		maxima[name] = max
		for i := 0; i < 4; i++ {
			if name == ledgerName(i) {
				s.last[i] = max
				if max > s.last[s.ledgerSlot] {
					s.ledgerSlot = i
				}
			}
		}
	}
	for _, stream := range []string{"stdout", "stderr"} {
		// A newly rotated slot is smaller but newer. Physical size is not order.
		if maxima[streamName(stream, 1)] > maxima[streamName(stream, 0)] {
			if stream == "stdout" {
				s.outSlot = 1
			} else {
				s.errSlot = 1
			}
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	validated := events[:0]
	var previous uint64
	for _, e := range events {
		if e.Sequence <= previous {
			s.state.Partial = true
			s.newLoss = true
			continue
		}
		if previous != 0 && e.Sequence != previous+1 && !s.state.Partial {
			s.state.Partial = true
			s.newLoss = true
		}
		validated = append(validated, e)
		previous = e.Sequence
	}
	if observedSequence > s.state.Sequence {
		s.state.Sequence = observedSequence
		s.state.Partial = true
		s.newLoss = true
	}
	if s.state.Sequence > previous && s.state.Clean {
		s.state.Partial = true
		s.newLoss = true
	}
	s.state.Partial = s.state.Partial || s.newLoss
	if !metadataValid || unclean {
		s.state.Generation = randomID()
		s.state.LossEpoch++
		if unclean || len(events) > 0 {
			s.state.Partial = true
		}
	} else if s.newLoss {
		s.state.LossEpoch++
	}
	if !metadataValid {
		s.state.CursorKey = randomID()
	}
	s.state.Schema = 1
	s.state.ObservedBoots++
	s.state.Clean = false
	if err := s.checkpoint(); err != nil {
		return nil, nil, false, err
	}
	ok = true
	return s, validated, unclean, nil
}
func (s *storage) checkpoint() error {
	if s.state.Revision == ^uint64(0) {
		return errors.New("observation revision exhausted")
	}
	s.state.Revision++
	b, err := json.Marshal(s.state)
	if err != nil || len(b) > 4096 {
		return errors.New("invalid observation metadata")
	}
	// Two fixed <=4 KiB leaves include staging. Sync the inactive leaf then
	// rename it over the current leaf; the previous committed bytes survive
	// a failed write, with no third metadata file or unbounded orphan names.
	return replaceAtomic(s.dir, fmt.Sprintf("state-%d.json", s.stateSlot), fmt.Sprintf("state-%d.json", 1-s.stateSlot), b, 4096)
}
func (s *storage) sealManifest() error {
	for index, name := range dataNames() {
		b, err := readPrivate(s.dir, name, SegmentBytes)
		if missing(err) {
			s.state.Files[index] = segmentState{}
			continue
		}
		if err != nil {
			return err
		}
		s.state.Files[index] = segmentInfo(b)
	}
	return nil
}
func (s *storage) append(e Event, data []byte) (uint64, error) {
	if s.appendFault != nil {
		if err := s.appendFault(); err != nil {
			return 0, err
		}
	}
	// Reserve sequence before publishing bytes. A crash between writes is an
	// explicit gap, never a reused identity after restart.
	if e.Sequence > s.reserved {
		if e.Sequence > ^uint64(0)-255 {
			return 0, errors.New("observation sequence exhausted")
		}
		reserved := e.Sequence + 255
		s.state.Sequence = reserved
		s.state.Clean = false
		if err := s.checkpoint(); err != nil {
			return 0, err
		}
		s.reserved = reserved
	}
	name := ledgerName(s.ledgerSlot)
	var evicted uint64
	if s.lengths[name]+len(data) > SegmentBytes {
		s.ledgerSlot = (s.ledgerSlot + 1) % 4
		name = ledgerName(s.ledgerSlot)
		evicted = s.last[s.ledgerSlot]
		if err := replacePrivate(s.dir, name, nil); err != nil {
			return evicted, err
		}
		s.lengths[name] = 0
		s.last[s.ledgerSlot] = 0
	}
	if err := appendPrivate(s.dir, name, data, s.lengths[name]); err != nil {
		return evicted, err
	}
	s.lengths[name] += len(data)
	s.last[s.ledgerSlot] = e.Sequence
	slot := &s.outSlot
	if e.Stream == "stderr" {
		slot = &s.errSlot
	}
	name = streamName(e.Stream, *slot)
	if s.lengths[name]+len(data) > SegmentBytes {
		*slot = (*slot + 1) % 2
		name = streamName(e.Stream, *slot)
		if err := replacePrivate(s.dir, name, nil); err != nil {
			return evicted, err
		}
		s.lengths[name] = 0
	}
	if err := appendPrivate(s.dir, name, data, s.lengths[name]); err != nil {
		return evicted, err
	}
	s.lengths[name] += len(data)
	return evicted, nil
}
func (s *storage) close() {
	if s.lock != nil {
		s.lock.Close()
	}
	if s.dir != nil {
		s.dir.Close()
	}
}
func (s *storage) syncFiles() error {
	for name := range s.lengths {
		f, err := openForSync(s.dir, name)
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	return s.dir.Sync()
}
