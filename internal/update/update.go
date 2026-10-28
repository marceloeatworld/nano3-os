// Package update finds the latest nano3-os release on GitHub and fetches its nano3d
// binary for the miner, checked against the SHA-256 the release publishes. nano3d
// itself never writes the flash: the binary is staged in /tmp and the start script
// swaps it in after nano3d exits (slot A is too full to hold two binaries).
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/crypto/x509roots/fallback" // the miner's rootfs has no CA bundle
)

const (
	// Repo is the GitHub repository whose releases carry the binary.
	Repo = "marceloeatworld/nano3-os"
	// Asset is the release file built for the miner by the workflow, and Sum its SHA-256.
	Asset = "nano3d-riscv64"
	Sum   = Asset + ".sha256"
	// Staged is where the verified binary waits for the start script, and StagedSum the
	// companion checksum it checks again (BusyBox sha256sum -c format).
	Staged    = "/tmp/nano3d.new"
	StagedSum = Staged + ".sha256"

	maxBinary = 32 << 20 // a nano3d binary is about 7.5 MB
	maxText   = 1 << 20
)

// Release is one GitHub release with the two files the miner needs.
type Release struct {
	Tag       string    // "v0.1.0"
	URL       string    // the release page, for the dashboard link
	Published time.Time // when the release was published
	binary    string    // download URLs
	sum       string
}

// Newer reports whether tag (vX.Y.Z) is newer than the running version. A running
// version that is not a release tag (a development build) is never updated by itself.
func Newer(current, tag string) bool {
	c, okc := parse(current)
	t, okt := parse(tag)
	if !okc || !okt {
		return false
	}
	for i := range c {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

// parse reads "vX.Y.Z" (a "v" prefix optional, no pre-release suffix).
func parse(tag string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func client() *http.Client {
	return &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second, Proxy: http.ProxyFromEnvironment}}
}

func get(ctx context.Context, c *http.Client, url, accept, agent string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", agent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp, nil
}

// Latest asks the GitHub API for the newest release that carries the miner's binary
// and its checksum. agent names the caller in the request (GitHub requires one).
func Latest(ctx context.Context, agent string) (Release, error) {
	c := client()
	defer c.CloseIdleConnections()
	resp, err := get(ctx, c, "https://api.github.com/repos/"+Repo+"/releases/latest", "application/vnd.github+json", agent)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	var body struct {
		Tag       string    `json:"tag_name"`
		URL       string    `json:"html_url"`
		Published time.Time `json:"published_at"`
		Draft     bool      `json:"draft"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxText)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("release list: %w", err)
	}
	r := Release{Tag: body.Tag, URL: body.URL, Published: body.Published}
	for _, a := range body.Assets {
		switch a.Name {
		case Asset:
			r.binary = a.URL
		case Sum:
			r.sum = a.URL
		}
	}
	if _, ok := parse(r.Tag); !ok || body.Draft {
		return Release{}, fmt.Errorf("release %q: not a vX.Y.Z release", r.Tag)
	}
	if r.binary == "" || r.sum == "" {
		return Release{}, fmt.Errorf("release %s has no %s and %s", r.Tag, Asset, Sum)
	}
	return r, nil
}

// Download fetches the release's binary into Staged, with its checksum next to it in
// StagedSum, and returns an error (leaving nothing behind) when the SHA-256 does not
// match. progress, when not nil, gets the bytes done and the total (0 when unknown).
func Download(ctx context.Context, r Release, agent string, progress func(done, total int64)) error {
	c := client()
	defer c.CloseIdleConnections()
	want, err := fetchSum(ctx, c, r.sum, agent)
	if err != nil {
		return err
	}
	tmp := Staged + ".part"
	os.Remove(Staged)
	os.Remove(StagedSum)
	err = fetchBinary(ctx, c, r.binary, agent, tmp, want, progress)
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.WriteFile(StagedSum, []byte(want+"  "+filepath.Base(Staged)+"\n"), 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, Staged); err != nil {
		os.Remove(tmp)
		os.Remove(StagedSum)
		return err
	}
	return nil
}

// fetchSum reads the published checksum file ("<hex>  nano3d-riscv64").
func fetchSum(ctx context.Context, c *http.Client, url, agent string) (string, error) {
	resp, err := get(ctx, c, url, "*/*", agent)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxText))
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == Asset && len(f[0]) == sha256.Size*2 {
			if _, err := hex.DecodeString(f[0]); err == nil {
				return strings.ToLower(f[0]), nil
			}
		}
	}
	return "", errors.New(Sum + ": no checksum for " + Asset)
}

// fetchBinary streams the binary to path, hashing it on the way, and keeps it only
// when the hash is the published one.
func fetchBinary(ctx context.Context, c *http.Client, url, agent, path, want string, progress func(done, total int64)) error {
	resp, err := get(ctx, c, url, "application/octet-stream", agent)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 64<<10)
	body := io.LimitReader(resp.Body, maxBinary+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				f.Close()
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if done > maxBinary {
				f.Close()
				return fmt.Errorf("%s: larger than %d MB", Asset, maxBinary>>20)
			}
			if progress != nil {
				progress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return rerr
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: SHA-256 %s, the release says %s", Asset, got[:16], want[:16])
	}
	return nil
}
