// Package poolstats reads what a Public Pool instance (github.com/benjamin-wilson/public-pool)
// reports for one worker: the hashrate the pool sees, the best share difficulty and
// when it last saw the worker. nano3d shows it next to its own numbers.
package poolstats

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBody bounds the pool's answer: a worker list is a few kB.
const maxBody = 1 << 20

// Worker is the pool's view of one worker.
type Worker struct {
	HashRate float64 // H/s, as the pool estimates it from accepted shares
	BestDiff float64
	LastSeen time.Time
}

// CAPool parses PEM certificates, such as the local root CA of a StartOS or Umbrel
// server, that sign the pool's HTTPS certificate. "" means the system roots.
func CAPool(pem string) (*x509.CertPool, error) {
	if pem == "" {
		return nil, nil
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("no PEM certificate found")
	}
	return p, nil
}

// Fetch asks the Public Pool at base (its web address, e.g. https://host:port) about the
// worker of a stratum user "address.worker". ca is the PEM text of the CA that signs
// the pool's HTTPS certificate, or "" for the system roots; the certificate is
// always verified.
func Fetch(base, ca, user string) (Worker, error) {
	roots, err := CAPool(ca)
	if err != nil {
		return Worker{}, err
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer tr.CloseIdleConnections() // one call a minute: no keep-alive worth keeping
	c := &http.Client{Timeout: 10 * time.Second, Transport: tr}
	addr, name, _ := strings.Cut(user, ".")
	resp, err := c.Get(strings.TrimRight(base, "/") + "/api/client/" + url.PathEscape(addr))
	if err != nil {
		return Worker{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Worker{}, fmt.Errorf("%s: %s", base, resp.Status)
	}
	body := io.LimitReader(resp.Body, maxBody)
	var r struct {
		Workers []struct {
			Name     string  `json:"name"`
			HashRate float64 `json:"hashRate"`
			BestDiff string  `json:"bestDifficulty"`
			LastSeen string  `json:"lastSeen"`
		} `json:"workers"`
	}
	if err := json.NewDecoder(body).Decode(&r); err != nil {
		return Worker{}, err
	}
	// A worker that reconnected can be listed once per session: take the latest.
	var w Worker
	found := false
	for _, x := range r.Workers {
		if x.Name != name {
			continue
		}
		seen, _ := time.Parse(time.RFC3339, x.LastSeen)
		if found && !seen.After(w.LastSeen) {
			continue
		}
		best, err := strconv.ParseFloat(x.BestDiff, 64)
		if err != nil || math.IsNaN(best) || math.IsInf(best, 0) { // "Infinity" parses without error; JSON cannot carry it
			best = 0
		}
		w, found = Worker{HashRate: x.HashRate, BestDiff: best, LastSeen: seen}, true
	}
	if !found {
		return Worker{}, fmt.Errorf("pool lists no worker %q for %s", name, addr)
	}
	return w, nil
}
