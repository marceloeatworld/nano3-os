package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Overrides are the settings changed from nano3d's web page or front button. They
// live in nano3d's own file (normally /data/nano3-os/config.json) and take precedence
// over the stock configuration, which nano3d never writes: the stock firmware keeps
// its own settings for a rollback.
type Overrides struct {
	Pools      []Pool `json:"pools,omitempty"`
	Level      *int   `json:"level,omitempty"`
	Night      string `json:"night,omitempty"` // HH:MM-HH:MM, "" for no night mode
	NightLevel int    `json:"night_level"`
	TZ         string `json:"tz,omitempty"`
	// PoolAPI is the web address of a Public Pool instance to read the pool's view of
	// this worker from, "" for none; PoolAPICA is the PEM CA that signs its HTTPS
	// certificate, "" for the system roots.
	PoolAPI   string `json:"pool_api,omitempty"`
	PoolAPICA string `json:"pool_api_ca,omitempty"`
	// PriceKWh is the electricity price per kWh in Currency, for the cost shown on the
	// web page; 0 hides it.
	PriceKWh float64 `json:"price_kwh,omitempty"`
	Currency string  `json:"currency,omitempty"`
	// WebPassword guards the settings on the web page (user "root"); "" means
	// "root", the stock web login.
	WebPassword string `json:"web_password,omitempty"`
	// CoinbaseTag is up to 4 ASCII characters written into the part of the pool's
	// extranonce2 the K210 never touches (bytes 5 to 8 of an 8-byte extranonce2), so
	// a block this miner finds carries them in its coinbase. "" for none.
	CoinbaseTag string `json:"coinbase_tag,omitempty"`
	// FanFixed is a minimum fan duty in percent for the day (10..100), under the stock
	// PID, which still raises the fan when the chips need it; 0 means no floor. The
	// night window always runs the PID alone.
	FanFixed int `json:"fan_fixed,omitempty"`
	// FanTarget is the day PID target in deg C (50..100); 0 means the MM's own target
	// (80, 85, 90 C for levels 0, 1, 2). The night window always uses the MM's.
	FanTarget int `json:"fan_target,omitempty"`
	// NoUpdateCheck turns off the daily question to GitHub for a new release (the
	// dashboard then never offers an update). Off by default: the check runs.
	NoUpdateCheck bool `json:"no_update_check,omitempty"`
	// LevelTHs is the hashrate nano3d measured at each work level (index = level),
	// recorded after 10 minutes at a steady state, for the level planner on the page.
	LevelTHs []float64 `json:"level_ths,omitempty"`
}

// LoadOverrides reads the overrides file. A missing file means no overrides.
func LoadOverrides(path string) (Overrides, error) {
	var o Overrides
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	return o, json.Unmarshal(b, &o)
}

// Save writes the overrides file atomically (a temporary file renamed over it), so a
// power cut leaves either the old or the new file.
func (o Overrides) Save(path string) error {
	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// The rename itself reaches the flash only once the directory is synced.
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
