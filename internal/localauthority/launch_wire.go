package localauthority

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"

	"gitcode-mcp/internal/authority"
)

type launchMessage struct {
	Schema     int            `json:"schema"`
	Protocol   string         `json:"protocol"`
	Profile    string         `json:"profile"`
	Phase      string         `json:"phase"`
	Challenge  authority.UUID `json:"challenge"`
	Generation authority.UUID `json:"generation,omitempty"`
	Binding    string         `json:"binding,omitempty"`
}

func (m *launchMessage) Validate() error {
	if m.Schema != 1 || m.Protocol != authority.Protocol || m.Profile != strictLaunchProfile || !m.Challenge.Valid() {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch m.Phase {
	case "challenge":
		if m.Generation != "" || m.Binding != "" {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "ready", "activate", "active":
		if !m.Generation.Valid() || !validDigest(m.Binding) {
			return authority.Error{Code: authority.InvalidContract}
		}
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func launchReply(f launchFrame, binding, phase string) launchMessage {
	return launchMessage{1, authority.Protocol, strictLaunchProfile, phase, f.Challenge, f.Generation, binding}
}

func expectLaunch(conn *net.UnixConn, f launchFrame, binding, phase string) error {
	var m launchMessage
	if readLaunch(conn, &m) != nil || m != launchReply(f, binding, phase) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func readLaunch(r io.Reader, target any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return authority.Error{Code: authority.Unavailable}
	}
	n := binary.BigEndian.Uint32(size[:])
	if n < 2 || n > launchFrameLimit {
		return authority.Error{Code: authority.InvalidContract}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return authority.Error{Code: authority.Unavailable}
	}
	return authority.DecodeJSON(body, target)
}

func writeLaunch(w io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil || len(body) > launchFrameLimit {
		return authority.Error{Code: authority.InvalidContract}
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(body)))
	frame := append(header[:], body...)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil || n <= 0 {
			return authority.Error{Code: authority.Unavailable}
		}
		frame = frame[n:]
	}
	return nil
}

func launchHandshake(ctx context.Context, conn *net.UnixConn) (context.Context, func(), error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if conn == nil || sameLaunchUser(conn) != nil {
		return nil, nil, authority.Error{Code: authority.PolicyDenied}
	}
	ctx, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		cancel()
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(joined) })
	release := func() {
		if !stop() {
			<-joined
		}
		cancel()
	}
	return ctx, release, nil
}
