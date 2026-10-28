// lcdshot renders the LCD frame nano3d would draw for a miner's current status, as a
// PNG: a screenshot of the front display without a camera, or a preview of the design
// on the PC. It reads /api/status and /api/history from the miner's web page.
//
//	go run ./cmd/lcdshot -miner http://<miner> -o lcd.png
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"time"

	"nano3-os/internal/screen"
)

func get(base, path string, v any) error {
	c := &http.Client{Timeout: 10 * time.Second}
	resp, err := c.Get(base + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s%s: %s", base, path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func main() {
	miner := flag.String("miner", "", "the miner's web address, e.g. http://192.168.1.20 (required)")
	out := flag.String("o", "lcd.png", "output PNG")
	flag.Parse()
	if *miner == "" {
		fmt.Fprintln(os.Stderr, "lcdshot: -miner is required")
		flag.Usage()
		os.Exit(2)
	}
	var st struct {
		THs       float64 `json:"ths"`
		Expected  float64 `json:"expected_ths"`
		PowerW    int     `json:"power_w"`
		JPerTH    float64 `json:"j_per_th"`
		TempMean  float32 `json:"temp_mean"`
		TempMax   float32 `json:"temp_max"`
		FanPct    int     `json:"fan_pct"`
		Level     int     `json:"level"`
		Night     bool    `json:"night"`
		Safety    string  `json:"safety"`
		Pool      string  `json:"pool"`
		Accepted  int     `json:"accepted"`
		Rejected  int     `json:"rejected"`
		Best      int     `json:"best_zero_bits"`
		IP        string  `json:"ip"`
		UptimeS   int64   `json:"uptime_s"`
		PoolView  bool    `json:"pool_view"`
		PoolTHs   float64 `json:"pool_seen_ths"`
		PoolBest  float64 `json:"pool_best_diff"`
		PoolErr   string  `json:"pool_view_error"`
		PoolSeenS int64   `json:"pool_last_seen_s"`
	}
	var hist []struct {
		THs float64 `json:"ths"`
	}
	if err := get(*miner, "/api/status", &st); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := get(*miner, "/api/history", &hist); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var samples []float64
	for _, h := range hist {
		samples = append(samples, h.THs)
	}
	img, err := screen.Render(screen.Status{TH: st.THs, Expected: st.Expected, PowerW: st.PowerW, JPerTH: st.JPerTH,
		TempMean: st.TempMean, TempMax: st.TempMax, Fan: st.FanPct, Level: st.Level, Night: st.Night,
		Hot: st.Safety != "ok", Safety: st.Safety, Pool: st.Pool, Accepted: st.Accepted, Rejected: st.Rejected,
		IP: st.IP, UpS: st.UptimeS, PoolView: st.PoolView && st.PoolErr == "" && st.PoolSeenS >= 0,
		PoolTHs: st.PoolTHs, PoolBest: st.PoolBest, Hist: samples, Best: st.Best})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
