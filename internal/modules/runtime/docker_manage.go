package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/policy"
)

var _ Manager = (*Docker)(nil)

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	imageRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@+-]{0,254}$`)
)

// call performs one Engine API request. It returns the HTTP status so callers can treat 404/304 specially;
// non-2xx statuses other than those the caller handles come back as errors by the helpers below.
func (d *Docker) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != 304 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, fmt.Errorf("docker %s %s: %s %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	if out != nil && resp.StatusCode != 204 && resp.StatusCode != 304 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}

func checkName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid name %q", n)
	}
	return nil
}

func checkID(id string) error {
	if !containerRe.MatchString(id) && !nameRe.MatchString(id) {
		return fmt.Errorf("invalid container id %q", id)
	}
	return nil
}

func (d *Docker) EnsureNetwork(ctx context.Context, name string) error {
	if err := checkName(name); err != nil {
		return err
	}
	st, err := d.call(ctx, "GET", "/networks/"+url.PathEscape(name), nil, nil)
	if err == nil {
		return nil
	}
	if st != 404 {
		return err
	}
	_, err = d.call(ctx, "POST", "/networks/create", map[string]any{
		"Name": name, "Driver": "bridge", "CheckDuplicate": true,
		"Labels": map[string]string{LabelManaged: "true"},
	}, nil)
	if err != nil && strings.Contains(err.Error(), "409") { // lost a race with another create: fine
		return nil
	}
	return err
}

func (d *Docker) CreateContainer(ctx context.Context, a policy.Approved) (string, error) {
	if !a.Valid() {
		return "", fmt.Errorf("refusing to create a container that did not pass the isolation policy")
	}
	c := a.Container()
	if err := checkName(c.Name); err != nil {
		return "", err
	}
	if !imageRe.MatchString(c.Image) {
		return "", fmt.Errorf("invalid image reference %q", c.Image)
	}
	labels := map[string]string{}
	for k, v := range c.Labels {
		labels[k] = v
	}
	labels[LabelManaged] = "true"
	mounts := []map[string]any{}
	for _, m := range c.Mounts {
		mm := map[string]any{"Type": m.Type, "Target": m.Target, "ReadOnly": m.ReadOnly}
		if m.Type != "tmpfs" {
			mm["Source"] = m.Source
		}
		mounts = append(mounts, mm)
	}
	exposed := map[string]struct{}{}
	bindings := map[string][]map[string]string{}
	for _, p := range c.Ports {
		parts := strings.Split(p, ":")
		var ip, host, cont string
		switch len(parts) {
		case 2:
			host, cont = parts[0], parts[1]
		case 3:
			ip, host, cont = parts[0], parts[1], parts[2]
		default:
			return "", fmt.Errorf("invalid published port %q", p)
		}
		key := strings.Split(cont, "/")[0] + "/tcp"
		exposed[key] = struct{}{}
		bindings[key] = append(bindings[key], map[string]string{"HostIp": ip, "HostPort": host})
	}
	restart := map[string]any{"Name": c.Restart}
	if c.Restart == "" || c.Restart == "no" {
		restart = map[string]any{"Name": "no"}
	}
	host := map[string]any{
		"Mounts": mounts, "Memory": c.MemoryBytes, "MemorySwap": c.MemoryBytes, "NanoCpus": c.NanoCPUs, "PidsLimit": c.PidsLimit,
		"CapDrop": c.CapDrop, "CapAdd": c.CapAdd, "SecurityOpt": c.SecurityOpt, "RestartPolicy": restart,
		"NetworkMode": c.Network, "Privileged": false, "ReadonlyRootfs": c.ReadonlyRootfs, "PortBindings": bindings,
	}
	body := map[string]any{
		"Image": c.Image, "Env": c.Env, "Labels": labels, "User": c.User, "ExposedPorts": exposed,
		"HostConfig": host,
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{
			c.Network: map[string]any{"Aliases": c.Aliases},
		}},
	}
	if len(c.Cmd) > 0 {
		body["Cmd"] = c.Cmd
	}
	var out struct{ Id string }
	if _, err := d.call(ctx, "POST", "/containers/create?name="+url.QueryEscape(c.Name), body, &out); err != nil {
		return "", err
	}
	return out.Id, nil
}

func (d *Docker) StartContainer(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	_, err := d.call(ctx, "POST", "/containers/"+id+"/start", nil, nil)
	return err
}

func (d *Docker) StopContainer(ctx context.Context, id string, grace int) error {
	if err := checkID(id); err != nil {
		return err
	}
	st, err := d.call(ctx, "POST", "/containers/"+id+"/stop?t="+strconv.Itoa(grace), nil, nil)
	if st == 404 {
		return nil
	}
	return err
}

func (d *Docker) KillContainer(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	st, err := d.call(ctx, "POST", "/containers/"+id+"/kill", nil, nil)
	if st == 404 || st == 409 { // gone, or already not running
		return nil
	}
	return err
}

func (d *Docker) RemoveContainer(ctx context.Context, id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	st, err := d.call(ctx, "DELETE", "/containers/"+id+"?force=1&v=0", nil, nil)
	if st == 404 {
		return nil
	}
	return err
}

type inspectBody struct {
	ID    string
	Name  string
	Image string
	State struct {
		Status     string
		Running    bool
		ExitCode   int
		OOMKilled  bool
		StartedAt  string
		FinishedAt string
		Health     *struct{ Status string }
	}
	Config          struct{ Labels map[string]string }
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress string }
	}
}

func parseT(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	if t.Year() < 2 {
		return time.Time{}
	}
	return t
}

func (d *Docker) InspectContainer(ctx context.Context, id, network string) (State, error) {
	if err := checkID(id); err != nil {
		return State{}, err
	}
	var b inspectBody
	st, err := d.call(ctx, "GET", "/containers/"+id+"/json", nil, &b)
	if st == 404 {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, err
	}
	s := State{ID: b.ID, Name: strings.TrimPrefix(b.Name, "/"), Image: b.Image, Status: b.State.Status, Running: b.State.Running,
		ExitCode: b.State.ExitCode, OOMKilled: b.State.OOMKilled, Labels: b.Config.Labels,
		StartedAt: parseT(b.State.StartedAt), FinishedAt: parseT(b.State.FinishedAt)}
	if b.State.Health != nil {
		s.Health = b.State.Health.Status
	}
	if n, ok := b.NetworkSettings.Networks[network]; ok {
		s.IP = n.IPAddress
	}
	return s, nil
}

func (d *Docker) ListContainers(ctx context.Context, labelKey string) ([]State, error) {
	f, _ := json.Marshal(map[string][]string{"label": {labelKey}})
	var list []struct {
		Id     string
		Names  []string
		Image  string
		State  string
		Labels map[string]string
	}
	if _, err := d.call(ctx, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(f)), nil, &list); err != nil {
		return nil, err
	}
	out := make([]State, 0, len(list))
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, State{ID: c.Id, Name: name, Image: c.Image, Status: c.State, Running: c.State == "running", Labels: c.Labels})
	}
	return out, nil
}

func (d *Docker) WaitContainer(ctx context.Context, id string) (int, error) {
	if err := checkID(id); err != nil {
		return 0, err
	}
	var out struct{ StatusCode int }
	if _, err := d.call(ctx, "POST", "/containers/"+id+"/wait?condition=not-running", nil, &out); err != nil {
		return 0, err
	}
	return out.StatusCode, nil
}

func (d *Docker) ImageInspect(ctx context.Context, ref string) (ImageInfo, error) {
	if !imageRe.MatchString(ref) {
		return ImageInfo{}, fmt.Errorf("invalid image reference %q", ref)
	}
	var b struct {
		Id          string
		RepoDigests []string
		Size        int64
	}
	st, err := d.call(ctx, "GET", "/images/"+ref+"/json", nil, &b)
	if st == 404 {
		return ImageInfo{}, ErrNotFound
	}
	if err != nil {
		return ImageInfo{}, err
	}
	return ImageInfo{ID: b.Id, RepoDigests: b.RepoDigests, SizeBytes: b.Size}, nil
}

func (d *Docker) TagImage(ctx context.Context, src, repo, tag string) error {
	if !imageRe.MatchString(src) || !imageRe.MatchString(repo) || !nameRe.MatchString(tag) {
		return fmt.Errorf("invalid image tag arguments")
	}
	_, err := d.call(ctx, "POST", "/images/"+src+"/tag?repo="+url.QueryEscape(repo)+"&tag="+url.QueryEscape(tag), nil, nil)
	return err
}

func (d *Docker) RemoveImage(ctx context.Context, ref string) error {
	if !imageRe.MatchString(ref) {
		return fmt.Errorf("invalid image reference %q", ref)
	}
	st, err := d.call(ctx, "DELETE", "/images/"+ref+"?noprune=0", nil, nil)
	if st == 404 || st == 409 { // gone, or still used by a container: not an error for retention
		return nil
	}
	return err
}

func (d *Docker) Prune(ctx context.Context) (PruneResult, error) {
	var res PruneResult
	cf, _ := json.Marshal(map[string][]string{"label": {LabelManaged + "=true"}})
	var c struct {
		ContainersDeleted []string
		SpaceReclaimed    int64
	}
	if _, err := d.call(ctx, "POST", "/containers/prune?filters="+url.QueryEscape(string(cf)), nil, &c); err != nil {
		return res, err
	}
	res.ContainersRemoved, res.ReclaimedBytes = len(c.ContainersDeleted), c.SpaceReclaimed
	var im struct {
		ImagesDeleted  []json.RawMessage
		SpaceReclaimed int64
	}
	if _, err := d.call(ctx, "POST", "/images/prune?filters="+url.QueryEscape(`{"dangling":["true"]}`), nil, &im); err != nil {
		return res, err
	}
	res.ImagesRemoved, res.ReclaimedBytes = len(im.ImagesDeleted), res.ReclaimedBytes+im.SpaceReclaimed
	var bc struct{ SpaceReclaimed int64 }
	if _, err := d.call(ctx, "POST", "/build/prune", nil, &bc); err == nil {
		res.BuildCacheBytes, res.ReclaimedBytes = bc.SpaceReclaimed, res.ReclaimedBytes+bc.SpaceReclaimed
	}
	return res, nil
}
