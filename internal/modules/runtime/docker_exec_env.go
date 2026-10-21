package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ExecWithEnv creates and attaches a non-tty exec with the given environment. Cmd and Env must contain no NUL;
// it is caller-supplied data and this is the boundary that says so.
func (d *Docker) ExecWithEnv(ctx context.Context, container string, cmd, env []string) (ExecStream, error) {
	if err := checkID(container); err != nil {
		return nil, err
	}
	if len(cmd) == 0 {
		return nil, errors.New("exec needs a command")
	}
	// A NUL is the one byte the daemon cannot carry: it ends the argument inside execve, so a password or a
	// filename truncated by one fails as a wrong-credentials or missing-file error far away from here.
	if err := checkNoNul("exec cmd", cmd...); err != nil {
		return nil, err
	}
	if err := checkNoNul("exec env", env...); err != nil {
		return nil, err
	}

	// The create-and-attach sequence below is Exec's, copied rather than shared. Exec is the frozen P3 terminal
	// path and its file is out of bounds this phase, so folding the two into one helper would mean editing the code
	// every browser shell runs through; eight lines that cannot drift into a bug are the cheaper risk. They differ
	// exactly where the two uses differ: Tty is false, the environment is the caller's (a dump password goes in it
	// rather than into argv, where the panel's redaction of logged argv would never see it), and an initial
	// terminal size means nothing without a tty, so no resize is attempted.
	body, err := json.Marshal(map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true,
		"Tty": false, "Cmd": cmd, "Env": env})
	if err != nil {
		return nil, fmt.Errorf("docker exec create: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/containers/"+container+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var created struct{ Id string }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 || err != nil || !containerRe.MatchString(created.Id) {
		return nil, fmt.Errorf("docker exec create: %s", resp.Status)
	}

	// Tty false makes the daemon multiplex stdout and stderr into 8-byte-framed chunks, so Stdout() hands back
	// that framed stream and a caller reading a binary dump has to unframe it. demux in docker.go cannot do that
	// for them: it splits on newlines and strips what looks like a timestamp prefix, which is log handling and
	// would corrupt file bytes.
	conn, br, err := d.hijack(ctx, "/exec/"+created.Id+"/start", []byte(`{"Detach":false,"Tty":false}`))
	if err != nil {
		return nil, err
	}
	// dockerExec is reused as is: it already satisfies the frozen ExecStream contract, and its Close sends the
	// terminal's EOF gesture before asking for the kill. Those three bytes land in the exec's stdin, which a dump
	// command never reads, so they are inert here.
	return &dockerExec{d: d, id: created.Id, conn: conn, br: br}, nil
}

// checkNoNul rejects any string holding a NUL, naming which group of values was checked. It deliberately does not
// quote the offending value: an env entry is a dump password, and errors reach the audit log and the operator.
func checkNoNul(what string, vals ...string) error {
	for i, v := range vals {
		if strings.IndexByte(v, 0) >= 0 {
			return fmt.Errorf("invalid %s: entry %d contains a NUL byte", what, i)
		}
	}
	return nil
}
