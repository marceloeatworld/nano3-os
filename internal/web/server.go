// Package web serves nano3d's status page and JSON API on the LAN, in place of the
// stock web UI that lives inside btcminer. Reading the status needs no login; the
// settings take HTTP basic auth (user root), as the stock UI has a root login.
//
// The handlers never touch the miner: the main loop publishes a Status every few
// seconds and receives validated Settings over a channel.
package web

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nano3-os/internal/config"
	"nano3-os/internal/control"
	"nano3-os/internal/poolstats"
)

var (
	//go:embed index.html
	indexHTML []byte
	//go:embed settings.html
	settingsHTML []byte
)

// Status is the live state shown on the page and returned by GET /api/status.
type Status struct {
	Version   string    `json:"version"`
	UptimeS   int64     `json:"uptime_s"`
	Safety    string    `json:"safety"`
	Night     bool      `json:"night"`      // the night window is in force (or the clock is not set yet)
	WarmingUp bool      `json:"warming_up"` // under 10 minutes since the MM came up: figures still settling
	Level     int       `json:"level"`
	MaxLevel  int       `json:"max_level"`
	THs       float64   `json:"ths"`
	TempMean  float32   `json:"temp_mean"`
	TempMax   float32   `json:"temp_max"`
	ASICTemps []float32 `json:"asic_temps"` // by ASIC index, 0 = no reading
	TargetC   int       `json:"target_temp"`
	FanPct    int       `json:"fan_pct"`   // -1 when nano3d does not drive the fan
	FanFixed  bool      `json:"fan_fixed"` // a minimum duty is in force under the PID
	Pool      string    `json:"pool"`      // "" while the pool is lost
	PoolUser  string    `json:"pool_user"`
	PoolDiff  float64   `json:"pool_diff"`
	Valid     int       `json:"valid"`
	Accepted  int       `json:"accepted"`
	Rejected  int       `json:"rejected"`
	Best      int       `json:"best_zero_bits"`
	IP        string    `json:"ip"`
	// The pool's view of this worker (Public Pool API), when one is configured.
	PoolView      bool    `json:"pool_view"`
	PoolSeenTHs   float64 `json:"pool_seen_ths"`
	PoolBestDiff  float64 `json:"pool_best_diff"`
	PoolLastSeenS int64   `json:"pool_last_seen_s"`
	PoolViewErr   string  `json:"pool_view_error,omitempty"`

	// Power, from the MM's 0x55 report (field meanings inferred, see internal/k210).
	PowerW      int       `json:"power_w"` // input power; -1 when no report for a minute
	InputV      float64   `json:"input_v"`
	InputA      float64   `json:"input_a"`    // power / voltage
	JPerTH      float64   `json:"j_per_th"`   // power / MM hashrate; 0 when unknown
	EnergyKWh   float64   `json:"energy_kwh"` // since nano3d started
	PriceKWh    float64   `json:"price_kwh"`
	Currency    string    `json:"currency"`
	Levels      []Level   `json:"levels"`       // what each work level would draw and hash
	ExpectedTHs float64   `json:"expected_ths"` // GHSspd
	HWErrPct    float64   `json:"hw_error_pct"` // DH
	PLLMHz      []int     `json:"pll_mhz"`
	ASICVolts   []float32 `json:"asic_volts"`   // by ASIC index, MM units
	ASICErrPct  []float64 `json:"asic_err_pct"` // by ASIC index

	Update Update `json:"update"`
}

// Update is the state of the release check and of a self-update under way, for the
// dashboard. The check is a daily question to GitHub; the Update button on the page
// starts the download (POST /api/update).
type Update struct {
	Current  string `json:"current"`  // release tag this binary was built for, "" for a build from the tree
	Latest   string `json:"latest"`   // newest release on GitHub, "" before the first successful check
	URL      string `json:"url"`      // its release page
	Newer    bool   `json:"newer"`    // Latest is newer than Current
	State    string `json:"state"`    // "", "downloading", "restarting" (verified, nano3d restarts on it) or "error"
	Progress int    `json:"progress"` // percent of the binary downloaded
	Error    string `json:"error,omitempty"`
	CheckedS int64  `json:"checked_s"` // seconds since the last successful check, -1 for none yet
}

// Level is the estimate for one work level, for choosing the level on the page.
type Level struct {
	Level  int     `json:"level"`
	Watts  int     `json:"watts"`   // nominal, from the MM's level table
	MHz    int     `json:"mhz"`     // mean core clock of the level
	EstTHs float64 `json:"est_ths"` // measured at that level, else expected from its clock, 0 when unknown
}

// Sample is one point of the history: the state once a minute.
type Sample struct {
	T       int64   `json:"t"` // Unix seconds
	THs     float64 `json:"ths"`
	PowerW  int     `json:"power_w"`
	TempMax float32 `json:"temp_max"`
	FanPct  int     `json:"fan_pct"`
	Level   int     `json:"level"`
}

// historyLen keeps 24 h of one-minute samples.
const historyLen = 24 * 60

// Settings are what the settings page reads and changes (GET and POST /api/settings).
type Settings struct {
	Level       int           `json:"level"`
	Night       string        `json:"night"` // HH:MM-HH:MM, "" for no night mode
	NightLevel  int           `json:"night_level"`
	TZ          string        `json:"tz"`
	Pools       []config.Pool `json:"pools"`
	PoolAPI     string        `json:"pool_api"`    // Public Pool web address, "" for none
	PoolAPICA   string        `json:"pool_api_ca"` // PEM CA of its HTTPS certificate
	PriceKWh    float64       `json:"price_kwh"`   // electricity price, 0 hides the cost
	Currency    string        `json:"currency"`
	CoinbaseTag string        `json:"coinbase_tag"` // up to 4 ASCII characters, "" for none
	FanFixed    int           `json:"fan_fixed"`    // day fan duty in percent, 0 for the PID
	FanTarget   int           `json:"fan_target"`   // day PID target in deg C, 0 for the MM's
	UpdateCheck bool          `json:"update_check"` // ask GitHub for a new release once a day
}

// Server holds the latest status and settings for the handlers.
type Server struct {
	password string
	status   atomic.Pointer[Status]
	settings atomic.Pointer[Settings]
	changes  chan Settings
	updates  chan struct{}

	mu      sync.Mutex
	history []Sample // oldest first, at most historyLen
}

// Record appends a history sample, dropping the oldest beyond 24 h.
func (s *Server) Record(x Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append(s.history, x)
	if len(s.history) > historyLen {
		s.history = append(s.history[:0], s.history[len(s.history)-historyLen:]...)
	}
}

// Hashrates returns the history's hashrate samples, oldest first, for the LCD chart.
func (s *Server) Hashrates() []float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]float64, len(s.history))
	for i, x := range s.history {
		out[i] = x.THs
	}
	return out
}

func (s *Server) getHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := append([]Sample{}, s.history...)
	s.mu.Unlock()
	writeJSON(w, h)
}

// New returns a server whose settings take the given password for user root.
func New(password string) *Server {
	return &Server{password: password, changes: make(chan Settings), updates: make(chan struct{}, 1)}
}

// Publish replaces the status the page shows.
func (s *Server) Publish(st Status) { s.status.Store(&st) }

// SetSettings replaces the settings the page shows.
func (s *Server) SetSettings(x Settings) { s.settings.Store(&x) }

// Changes delivers settings saved on the page, already validated.
func (s *Server) Changes() <-chan Settings { return s.changes }

// Updates delivers a signal each time the Update button on the page is pressed.
func (s *Server) Updates() <-chan struct{} { return s.updates }

// ListenAndServe serves until the listener fails.
func (s *Server) ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page(indexHTML))
	mux.HandleFunc("GET /settings", s.auth(page(settingsHTML)))
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("GET /api/history", s.getHistory)
	mux.HandleFunc("GET /api/settings", s.auth(s.getSettings))
	mux.HandleFunc("POST /api/settings", s.auth(s.postSettings))
	mux.HandleFunc("POST /api/update", s.auth(s.postUpdate))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func page(b []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	}
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "root" || subtle.ConstantTimeCompare([]byte(p), []byte(s.password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="nano3-os"`)
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil { // a NaN or Inf slipped through: better a 500 than an empty 200
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(append(b, '\n'))
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	st := s.status.Load()
	if st == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, st)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	x := s.settings.Load()
	if x == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, x)
}

func (s *Server) postSettings(w http.ResponseWriter, r *http.Request) {
	st, cur := s.status.Load(), s.settings.Load()
	if st == nil || cur == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	if !sameSite(w, r) {
		return
	}
	var x Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&x); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := Validate(&x, st.MaxLevel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	restart := !slices.Equal(x.Pools, cur.Pools)
	select {
	case s.changes <- x:
	case <-time.After(5 * time.Second):
		http.Error(w, "nano3d is busy, try again", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]bool{"ok": true, "restart": restart})
}

// sameSite refuses a POST that another site could have made. A page on another site
// can post a text/plain form carrying JSON, and the browser attaches the cached login;
// such a request cannot set this content type, and a browser names its origin.
func sameSite(w http.ResponseWriter, r *http.Request) bool {
	if ct, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); strings.TrimSpace(ct) != "application/json" {
		http.Error(w, "want Content-Type: application/json", http.StatusUnsupportedMediaType)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return false
		}
	}
	return true
}

// postUpdate starts the self-update to the latest release; the status reports its
// progress. The main loop ignores the request when no newer release is known.
func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	if !sameSite(w, r) {
		return
	}
	select {
	case s.updates <- struct{}{}:
	default: // one is already waiting for the main loop
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// Validate checks settings from the page and normalizes the pool addresses
// (a stock-style stratum+tcp:// prefix is accepted and removed).
func Validate(x *Settings, maxLevel int) error {
	level := func(name string, v int) error {
		if v < 0 || v >= maxLevel {
			return fmt.Errorf("%s %d: want 0..%d", name, v, maxLevel-1)
		}
		return nil
	}
	if err := level("level", x.Level); err != nil {
		return err
	}
	if err := level("night level", x.NightLevel); err != nil {
		return err
	}
	if x.Night != "" {
		if _, err := control.ParseWindow(x.Night); err != nil {
			return err
		}
	}
	if _, err := time.LoadLocation(x.TZ); err != nil {
		return fmt.Errorf("time zone %q: %v", x.TZ, err)
	}
	x.PoolAPI, x.PoolAPICA = strings.TrimSpace(x.PoolAPI), strings.TrimSpace(x.PoolAPICA)
	if x.PoolAPI != "" {
		u, err := url.Parse(x.PoolAPI)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("pool web address %q: want http(s)://host:port", x.PoolAPI)
		}
	}
	if _, err := poolstats.CAPool(x.PoolAPICA); err != nil {
		return fmt.Errorf("pool CA certificate: %v", err)
	}
	x.Currency = strings.TrimSpace(x.Currency)
	if x.PriceKWh < 0 || x.PriceKWh > 100 || len(x.Currency) > 8 {
		return errors.New("price: want 0..100 per kWh and a currency of at most 8 characters")
	}
	if x.FanFixed != 0 && (x.FanFixed < 10 || x.FanFixed > 100) {
		return errors.New("fan: want 0 (automatic) or 10..100 percent")
	}
	if x.FanTarget != 0 && (x.FanTarget < 50 || x.FanTarget > 100) {
		return errors.New("fan target: want 0 (the MM's) or 50..100 deg C")
	}
	x.CoinbaseTag = strings.TrimSpace(x.CoinbaseTag)
	if len(x.CoinbaseTag) > 4 || strings.ContainsFunc(x.CoinbaseTag, func(r rune) bool { return r < '!' || r > '~' }) {
		return errors.New("coinbase tag: want at most 4 plain ASCII characters, no spaces")
	}
	var pools []config.Pool
	for _, p := range x.Pools {
		p.Addr, p.User = strings.TrimSpace(p.Addr), strings.TrimSpace(p.User)
		if p.Addr == "" {
			continue
		}
		if strings.Contains(p.Addr, "://") {
			a, err := config.PoolAddr(p.Addr)
			if err != nil {
				return err
			}
			p.Addr = a
		}
		host, port, err := net.SplitHostPort(p.Addr)
		if n, perr := strconv.Atoi(port); err != nil || perr != nil || host == "" || n < 1 || n > 65535 {
			return fmt.Errorf("pool %q: want host:port", p.Addr)
		}
		if p.User == "" {
			return fmt.Errorf("pool %s: no user", p.Addr)
		}
		pools = append(pools, p)
	}
	if len(pools) == 0 || len(pools) > 3 {
		return errors.New("want 1 to 3 pools")
	}
	x.Pools = pools
	return nil
}
