//go:build darwin || linux

package observability

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// LegacyFile contains only an opaque metadata identity; neither path nor text.
type LegacyFile struct {
	Stream   string `json:"stream"`
	Present  bool   `json:"present"`
	Identity string `json:"identity,omitempty"`
	Bytes    int64  `json:"bytes"`
}
type LegacySample struct {
	Cursor  LegacyCursor `json:"cursor"`
	HasMore bool         `json:"has_more"`
	Codes   []Code       `json:"codes"`
	Bytes   int          `json:"bytes"`
	Partial bool         `json:"partial"`
	State   string       `json:"state"`
}
type LegacyCursor struct {
	Identity    string `json:"identity,omitempty"`
	Offset      int64  `json:"offset"`
	SkipPartial bool   `json:"skip_partial"`
}

func legacyIdentity(f *os.File) (string, int64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%d:%d", st.Dev, st.Ino, st.Uid, st.Mode)))
	return hex.EncodeToString(sum[:]), st.Size, nil
}
func LegacyInventory(path string) ([]LegacyFile, error) {
	dir, err := openDirectory(path, false)
	if missing(err) {
		return []LegacyFile{{Stream: "stdout"}, {Stream: "stderr"}}, nil
	}
	if err != nil {
		return nil, errors.New("unsafe legacy output directory")
	}
	defer dir.Close()
	files := []LegacyFile{}
	for _, stream := range []string{"stdout", "stderr"} {
		name := "service.out.log"
		if stream == "stderr" {
			name = "service.err.log"
		}
		f, err := openPrivate(dir, name, unix.O_RDONLY)
		if missing(err) {
			files = append(files, LegacyFile{Stream: stream})
			continue
		}
		if err != nil {
			return nil, errors.New("unsafe legacy output file")
		}
		id, size, err := legacyIdentity(f)
		f.Close()
		if err != nil {
			return nil, errors.New("legacy output inspection failed")
		}
		files = append(files, LegacyFile{Stream: stream, Present: true, Identity: id, Bytes: size})
	}
	return files, nil
}

// ReadLegacy never returns a raw line, even for private local consumers. A
// single pass reads <=64 KiB and recognizes exact catalog templates only.
func ReadLegacy(path, stream string) (LegacySample, error) {
	return ReadLegacyFrom(path, stream, LegacyCursor{})
}
func ReadLegacyFrom(path, stream string, position LegacyCursor) (LegacySample, error) {
	if position.Offset < 0 || position.Identity != "" && (len(position.Identity) != 64 || strings.Trim(position.Identity, "0123456789abcdef") != "") {
		return LegacySample{}, errors.New("invalid legacy position")
	}
	name := "service.out.log"
	if stream == "stderr" {
		name = "service.err.log"
	} else if stream != "stdout" {
		return LegacySample{}, errors.New("invalid legacy stream")
	}
	dir, err := openDirectory(path, false)
	if err != nil {
		return LegacySample{State: "unsupported"}, errors.New("legacy output unavailable")
	}
	defer dir.Close()
	f, err := openPrivate(dir, name, unix.O_RDONLY)
	if missing(err) {
		return LegacySample{State: "unsupported"}, nil
	}
	if err != nil {
		return LegacySample{State: "partial"}, errors.New("unsafe legacy output file")
	}
	defer f.Close()
	identity, size, err := legacyIdentity(f)
	if err != nil {
		return LegacySample{}, errors.New("legacy output inspection failed")
	}
	state := "legacy_unbounded"
	if position.Identity != "" && position.Identity != identity {
		state = "rotated"
		position = LegacyCursor{}
	}
	if size < position.Offset {
		state = "truncated"
		position = LegacyCursor{}
	}
	position.Identity = identity
	if _, err := f.Seek(position.Offset, io.SeekStart); err != nil {
		return LegacySample{}, errors.New("legacy seek failed")
	}
	sample := LegacySample{State: state, Cursor: position, Partial: state != "legacy_unbounded"}
	r := bufio.NewReaderSize(io.LimitReader(f, MaxPageBytes), MaxEventBytes)
	for {
		line, err := r.ReadSlice('\n')
		sample.Bytes += len(line)
		sample.Cursor.Offset += int64(len(line))
		if len(line) > 0 && !sample.Cursor.SkipPartial {
			code := LegacyOmitted
			if err == nil {
				for candidate, t := range catalog {
					if string(line) == t.message+"\n" {
						code = candidate
						break
					}
				}
			}
			sample.Codes = append(sample.Codes, code)
		}
		if err == nil {
			sample.Cursor.SkipPartial = false
		}
		if err == bufio.ErrBufferFull {
			sample.Partial = true
			sample.Cursor.SkipPartial = true
			continue
		}
		if err != nil {
			if err != io.EOF {
				return sample, errors.New("legacy read failed")
			}
			if len(line) > 0 {
				sample.Partial = true
				sample.Cursor.SkipPartial = true
			}
			break
		}
		if len(sample.Codes) >= MaxPageEvents {
			sample.Partial = true
			break
		}
	}
	sample.HasMore = size > sample.Cursor.Offset
	sample.Partial = sample.Partial || sample.HasMore
	return sample, nil
}

// RemoveLegacy is only called after explicit confirmation AND proof that the
// platform owner has stopped. Recheck metadata before destructive cleanup.
func RemoveLegacy(path string, expected []LegacyFile) error {
	dir, err := openDirectory(path, false)
	if missing(err) {
		for _, f := range expected {
			if f.Present {
				return errors.New("legacy output changed")
			}
		}
		return nil
	}
	if err != nil {
		return errors.New("unsafe legacy output directory")
	}
	defer dir.Close()
	for _, entry := range expected {
		name := "service.out.log"
		if entry.Stream == "stderr" {
			name = "service.err.log"
		} else if entry.Stream != "stdout" {
			return errors.New("invalid legacy stream")
		}
		f, err := openPrivate(dir, name, unix.O_RDONLY)
		if missing(err) && !entry.Present {
			continue
		}
		if err != nil {
			return errors.New("legacy output changed")
		}
		id, _, err := legacyIdentity(f)
		f.Close()
		if err != nil || !entry.Present || id != entry.Identity {
			return errors.New("legacy output changed")
		}
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
			return errors.New("legacy cleanup incomplete")
		}
	}
	return dir.Sync()
}
