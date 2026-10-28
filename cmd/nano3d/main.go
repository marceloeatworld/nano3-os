// nano3d is the nano3-os mining daemon for the Avalon Nano 3: it replaces the stock
// btcminer. It brings up the K210 (MM) over /dev/ttyS1, takes jobs from a Stratum V1
// pool, sends them to the MM, checks every nonce the MM reports and submits shares.
// Like btcminer it drives the fan from the ASIC temperatures, keeps the hardware
// watchdog fed, and (with -wifi) brings up the Wi-Fi from the stock configuration.
//
// It never writes the stock configuration, never sends a frequency, voltage or
// firmware-upgrade command, and holds an exclusive lock on the UART.

// No EDNS0 in DNS queries: the owner's home router answers a repeated query that
// carries EDNS0 with NXDOMAIN (tested live: 1 of 9 Ocean lookups resolved with it,
// 9 of 9 without), and BusyBox, which btcminer relies on, never sends it.
//
//go:debug netedns0=0
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // zone data for -tz, independent of the miner's /usr/share/zoneinfo

	"nano3-os/internal/board"
	"nano3-os/internal/config"
	"nano3-os/internal/control"
	"nano3-os/internal/k210"
	"nano3-os/internal/poolstats"
	"nano3-os/internal/screen"
	"nano3-os/internal/stratum"
	"nano3-os/internal/update"
	"nano3-os/internal/web"
	"nano3-os/internal/work"
)

// Exit codes: 0 done, 1 error, 2 bad arguments or config, 3 the MM asked for a reset
// (status 4 overheat or 7), 4 new pools saved on the web page, 5 a new release waits in
// /tmp/nano3d.new (the start script swaps it in). The caller (session script or boot
// script) restarts a miner.
const (
	exitMMReset = 3
	exitRestart = 4
	exitUpdate  = 5
)

var (
	errRestart = errors.New("pools changed on the web page: restarting to use them")
	errUpdate  = errors.New("new release staged in " + update.Staged + ": restarting on it")
	errSignal  = errors.New("signal: stopping")
)

// release is the tag the workflow built this binary for (-X main.release=vX.Y.Z); ""
// for a build from the tree, which the release check then leaves alone.
var release string

const (
	pollEvery   = 100 * time.Millisecond
	fanEvery    = 2 * time.Second  // misc_thread runs fan_pid_control every ~2 s
	feedEvery   = 10 * time.Second // btcminer's main loop feeds every 10 s
	statusEvery = 30 * time.Second
	// poolSilence: a pool that sends nothing for this long is treated as lost. Pools
	// send a job every 20 to 60 s (live); a dead peer is caught sooner by TCP keepalive.
	poolSilence = 5 * time.Minute
	// primaryRetry: while mining on a fallback pool, the first pool is tried again this
	// often, as cgminer does: after a power cut the owner's own pool is back later than
	// the miner.
	primaryRetry = 5 * time.Minute
	confiles     = "/mnt/heater/confiles" // stock wpa_supplicant.conf lives here

	// warmUp: for this long after a start the MM's own figures (expected hashrate,
	// error rate) are still settling and are not judged.
	warmUp = 10 * time.Minute
	// maxASICs bounds the ASIC index taken from a telemetry frame (10 ASICs live).
	maxASICs = 32
	// maxPending is how many unanswered share submits mean the pool is gone.
	maxPending = 1000

	// fullPowerDuty is the fan duty the PID settles at on level 2 (72 %, both under
	// stock btcminer and under nano3d, 10-minute run): the start point after a level rise.
	fullPowerDuty = 720

	// updateEvery is how often GitHub is asked for a new release (an unauthenticated
	// API call; the owner can turn the check off on the settings page).
	updateEvery = 24 * time.Hour

	// Electricity price used until one is saved on the web page: the Portuguese
	// regulated simple tariff (ERSE, 6.9 kVA), 0.1711 EUR/kWh from 1 October 2026
	// (checked on comparison sites on 27 October 2026), plus 23 % VAT.
	defaultPriceKWh = 0.21
	defaultCurrency = "EUR"
)

type options struct {
	uart     string
	pools    []config.Pool
	level    int
	seconds  int
	submit   bool
	suggest  float64
	fan      bool
	watchdog bool
	wifi     *board.WiFi
	screen   bool
	button   bool
	http     string // web page address, "" = off
	settings string // nano3d's own settings file
	ov       config.Overrides
	fanFixed int // percent, -1 = PID (stock "fan-spd")
	target   int // fan PID target in deg C, 0 = the MM's

	night      *control.Window // nil: no night mode
	nightLevel int
	loc        *time.Location
	maxTemp    float64
	cooldown   time.Duration
}

// isNight reports whether t falls in the night window. The board has no RTC: until
// NTP sets the clock (about 20 s after the Wi-Fi comes up) the time is 1970, so the
// schedule cannot be known: the night settings apply then, as the quiet choice.
func (o options) isNight(t time.Time) bool {
	return o.night != nil && (t.Year() < 2020 || o.night.Contains(t.In(o.loc)))
}

// levelAt is the scheduled work level: -night-level inside the night window, else -level.
func (o options) levelAt(t time.Time) int {
	if o.isNight(t) {
		return o.nightLevel
	}
	return o.level
}

// fanFixedAt is the minimum fan duty in force at t, or -1 for none: a floor under the
// PID is a day setting, the night window (chosen for quiet) runs the PID alone.
func (o options) fanFixedAt(t time.Time) int {
	if o.fanFixed >= 0 && !o.isNight(t) {
		return o.fanFixed
	}
	return -1
}

// fanTargetAt is the PID target in force at t: the owner's day target when set, else
// the MM's (mm). The night window always follows the MM's target.
func (o options) fanTargetAt(t time.Time, mm int) int {
	if o.target != 0 && !o.isNight(t) {
		return o.target
	}
	return mm
}

func main() {
	var o options
	flag.StringVar(&o.uart, "uart", "/dev/ttyS1", "K210 UART")
	configDir := flag.String("config", "/data/usrcon", "stock config dir: pools (cgminer.ini), work level and Wi-Fi (systemcfg.ini) come from here unless -pool / -level are given")
	pool := flag.String("pool", "", "pool host:port (overrides the stock pools)")
	user := flag.String("user", "", "pool user, e.g. <btc address>.<worker> (with -pool)")
	pass := flag.String("pass", "x", "pool password (with -pool)")
	flag.IntVar(&o.level, "level", 2, "K210 work level, 0..2 on the Nano 3 (below the max level the MM reports; default: the stock work_level, else 2)")
	flag.IntVar(&o.seconds, "seconds", 0, "stop after this many seconds (0: run until signalled)")
	flag.BoolVar(&o.submit, "submit", true, "submit shares that meet the pool difficulty")
	flag.Float64Var(&o.suggest, "suggest-diff", 0, "ask the pool for this difficulty (0: do not ask)")
	flag.BoolVar(&o.fan, "fan", true, "drive the fan with btcminer's PID (mean ASIC temperature against the MM's target)")
	flag.BoolVar(&o.watchdog, "watchdog", true, "arm the hardware watchdog (89 s) and feed it from the main loop")
	flag.BoolVar(&o.screen, "screen", true, "draw the status on the front LCD (/dev/fb0)")
	flag.BoolVar(&o.button, "button", true, "the front button cycles the work level in force, as stock")
	flag.IntVar(&o.fanFixed, "fan-fixed", -1, "minimum fan duty in percent by day, 10..100; the PID still goes higher when needed (-1: none)")
	flag.IntVar(&o.target, "target-temp", 0, "day fan PID target in deg C, 50..100 (0: the MM's target, 90 at level 2; the night window always uses the MM's)")
	night := flag.String("night", "", "daily window HH:MM-HH:MM that runs -night-level, e.g. 22:00-08:00 (empty: no night mode)")
	flag.IntVar(&o.nightLevel, "night-level", 0, "work level inside the -night window (0: quietest)")
	tz := flag.String("tz", "UTC", "time zone of the -night window, e.g. Europe/Lisbon")
	flag.Float64Var(&o.maxTemp, "max-temp", 95, "safety limit on the hottest ASIC, deg C: above it level 0 and fan 100 %; still above after 2 min, mining stops")
	flag.DurationVar(&o.cooldown, "halt-cooldown", 15*time.Minute, "how long mining stays stopped after an over-heat before one retry at level 0")
	flag.StringVar(&o.http, "http", ":80", "web page and JSON API address (empty: off)")
	flag.StringVar(&o.settings, "settings", "/data/nano3-os/config.json", "nano3d's own settings, written by the web page and the button; they override the stock config, flags override both")
	wifi := flag.Bool("wifi", false, "bring up Wi-Fi from the stock config first (when nano3d replaces btcminer at boot)")
	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })
	var err error
	if o.ov, err = config.LoadOverrides(o.settings); err != nil {
		log.Printf("%s: %v", o.settings, err)
		os.Exit(2)
	}
	if o.ov.Currency == "" { // no price saved yet
		o.ov.PriceKWh, o.ov.Currency = defaultPriceKWh, defaultCurrency
	}
	if !set["tz"] && o.ov.TZ != "" {
		*tz = o.ov.TZ
	}
	if !set["night"] && o.ov.Night != "" {
		*night = o.ov.Night
		if !set["night-level"] {
			o.nightLevel = o.ov.NightLevel
		}
	}
	if o.loc, err = time.LoadLocation(*tz); err != nil {
		log.Printf("-tz: %v", err)
		os.Exit(2)
	}
	if *night != "" {
		w, err := control.ParseWindow(*night)
		if err != nil {
			log.Printf("-night: %v", err)
			os.Exit(2)
		}
		o.night = &w
	}

	var stock config.Stock
	if *pool == "" || *wifi {
		if stock, err = config.LoadStock(*configDir); err != nil {
			log.Printf("stock config: %v (or give -pool and -user)", err)
			os.Exit(2)
		}
	}
	if *pool != "" {
		if *user == "" {
			flag.Usage()
			os.Exit(2)
		}
		o.pools = []config.Pool{{Addr: *pool, User: *user, Pass: *pass}}
	} else {
		o.pools = stock.Pools
		if !set["level"] && stock.WorkLevel >= 0 {
			o.level = stock.WorkLevel
		}
		if len(o.ov.Pools) > 0 {
			o.pools = o.ov.Pools
		}
	}
	if !set["level"] && o.ov.Level != nil {
		o.level = *o.ov.Level
	}
	if !set["fan-fixed"] && o.ov.FanFixed > 0 {
		o.fanFixed = o.ov.FanFixed
	}
	if !set["target-temp"] && o.ov.FanTarget > 0 {
		o.target = o.ov.FanTarget
	}
	if *wifi {
		if stock.Net.Proto != 0 {
			log.Printf("stock config uses a static IP setup (net_proto %d), which nano3d does not support", stock.Net.Proto)
			os.Exit(2)
		}
		o.wifi = &board.WiFi{SSID: stock.Net.SSID, Password: stock.Net.Password, Auth: stock.Net.Auth}
	}

	err = run(o)
	var mm mmResetError
	switch {
	case errors.As(err, &mm):
		log.Print(err)
		os.Exit(exitMMReset)
	case errors.Is(err, errRestart):
		log.Print(err)
		os.Exit(exitRestart)
	case errors.Is(err, errUpdate):
		log.Print(err)
		os.Exit(exitUpdate)
	case err != nil:
		log.Print(err)
		os.Exit(1)
	}
}

type mmResetError int

func (e mmResetError) Error() string {
	return fmt.Sprintf("MM status %d (4 overheat, 7 reboot request): reset requested, exiting", int(e))
}

type stats struct {
	polls, crcBad, status, records, unknownJob, valid, rewound, invalid int
	poolOK, submitted, accepted, rejected                               int
	best                                                                int
}

// connect tries the pools in order and returns the first that completes the handshake
// and sends a job. When none does it tries the list again every 5 s for up to wait:
// the network can be briefly down (a stock btcminer start restarts the Wi-Fi), and the
// stock miner keeps retrying too. A signal on stop (nil: none) ends it with errSignal.
func connect(pools []config.Pool, suggest float64, wait time.Duration, stop <-chan os.Signal) (*stratum.Client, error) {
	deadline := time.Now().Add(wait)
	for {
		var last error
		for _, p := range pools {
			log.Printf("pool %s as %s", p.Addr, p.User)
			pc, err := stratum.Dial(p.Addr, p.User, p.Pass)
			if err == nil {
				if err = pc.Handshake(suggest, 30*time.Second); err == nil {
					if err = ready(pc, stop); err == nil {
						return pc, nil
					}
				}
				pc.Close()
				if errors.Is(err, errSignal) {
					return nil, err
				}
			}
			log.Printf("pool %s: %v", p.Addr, err)
			last = err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no pool reachable: %w", last)
		}
		select {
		case <-stop:
			return nil, errSignal
		case <-time.After(5 * time.Second):
		}
	}
}

// ready waits for the pool's first job, then takes anything trailing it (a
// set_difficulty after authorize, a newer notify) so the first burst is current.
func ready(pc *stratum.Client, stop <-chan os.Signal) error {
	deadline := time.After(2 * time.Minute)
	for pc.Notify == nil {
		select {
		case m, ok := <-pc.In:
			if !ok {
				return pc.Err()
			}
			pc.Dispatch(m)
		case <-stop:
			return errSignal
		case <-deadline:
			return errors.New("no job from the pool after 2 minutes")
		}
	}
	trailing := time.After(5 * time.Second) // a chatty pool must not hold this forever
	for {
		select {
		case m, ok := <-pc.In:
			if !ok {
				return pc.Err()
			}
			pc.Dispatch(m)
		case <-trailing:
			return nil
		case <-time.After(500 * time.Millisecond):
			log.Printf("pool ready: extranonce1=%x n2size=%d vmask=%08x diff=%g",
				pc.Pool.ExtraNonce1, pc.Pool.N2Size, pc.Pool.VMask, pc.Pool.Difficulty)
			return nil
		}
	}
}

// version is the release tag and VCS revision nano3d was built from, for the web page.
func version() string {
	rev, dirty := "", false
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value[:min(7, len(s.Value))]
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if rev == "" {
		return "nano3-os dev"
	}
	if dirty {
		rev += "-dirty"
	}
	if release != "" {
		return "nano3-os " + release + " (" + rev + ")"
	}
	return "nano3-os " + rev
}

// levelEstimates gives, for each work level, its nominal power and the hashrate its
// clocks should give: the MM's expected hashrate (GHSspd) scaled by the level's mean
// core clock over the clock now. Live it gives 2.12, 3.16 and 4.02 TH/s for levels 0, 1
// and 2, against 2.23, 3.24 and 4.13 measured: about 3 % low.
// A level nano3d has measured (measured, by level) shows that figure instead.
func levelEstimates(levels []k210.Level, now [4]int, speedMHs uint32, measured []float64) []web.Level {
	var out []web.Level
	cur := k210.CoreMHz(now)
	for i, l := range levels {
		e := web.Level{Level: i, Watts: l.Watts, MHz: int(k210.CoreMHz(l.PLL) + 0.5)}
		if cur > 0 && speedMHs > 0 {
			e.EstTHs = float64(speedMHs) / 1e6 * k210.CoreMHz(l.PLL) / cur
		}
		if i < len(measured) && measured[i] > 0 {
			e.EstTHs = measured[i]
		}
		out = append(out, e)
	}
	return out
}

// byASIC lists per-ASIC readings by ASIC index; a missing index stays zero.
func byASIC[T any](m map[uint16]T) []T {
	n := 0
	for k := range m {
		n = max(n, int(k)+1)
	}
	out := make([]T, n)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// lastSeen is how long ago the pool last saw this worker, in seconds; -1 when unknown.
func lastSeen(w *poolstats.Worker) int64 {
	if w == nil || w.LastSeen.IsZero() || time.Now().Year() < 2020 {
		return -1
	}
	return max(int64(time.Since(w.LastSeen).Seconds()), 0) // the pool's clock may lead by a second
}

// wlanIP is the board's IPv4 address on wlan0, for the screen.
func wlanIP() string {
	ifc, err := net.InterfaceByName("wlan0")
	if err != nil {
		return ""
	}
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	return ""
}

// temps keeps the latest per-ASIC temperature from 0x54 frames.
type temps map[uint16]float32

// mean is get_miner_temp_avg: the average of the readings above 0, or -273 when
// there is none yet (the fan PID then holds 25 %).
func (t temps) mean() (mean, hottest float32) {
	var sum float32
	n := 0
	hottest = -273
	for _, v := range t {
		if v > 0 {
			sum += v
			n++
			hottest = max(hottest, v)
		}
	}
	if n == 0 {
		return -273, hottest
	}
	return sum / float32(n), hottest
}

func run(o options) error {
	var fan *board.Fan
	if o.fan {
		var err error
		if fan, err = board.InitFan(); err != nil {
			return fmt.Errorf("fan: %w", err)
		}
		// Nothing drives the fan once nano3d ends, whatever the reason: leave it at 100 %.
		defer fan.Set(1000)
	}
	var scr *screen.Screen
	if o.screen {
		var err error
		if scr, err = screen.Open("/dev/fb0"); err != nil {
			log.Printf("screen: %v: running without it", err)
		} else {
			defer scr.Close()
			defer scr.Message("stopped", "nano3d is not running", true) // stays on the LCD
		}
	}
	show := func(title, detail string) {
		if scr != nil {
			scr.Message(title, detail, false)
		}
	}
	show("starting", "")
	start, ver := time.Now(), version()
	var webs *web.Server
	var changes <-chan web.Settings
	var updates <-chan struct{}
	if o.http != "" {
		pw := o.ov.WebPassword
		if pw == "" {
			pw = "root"
		}
		webs = web.New(pw)
		changes, updates = webs.Changes(), webs.Updates()
		go func() { log.Printf("web: %v", webs.ListenAndServe(o.http)) }()
	}
	settings := func() web.Settings {
		night := ""
		if o.night != nil {
			night = o.night.String()
		}
		return web.Settings{Level: o.level, Night: night, NightLevel: o.nightLevel, TZ: o.loc.String(),
			Pools: o.pools, PoolAPI: o.ov.PoolAPI, PoolAPICA: o.ov.PoolAPICA,
			PriceKWh: o.ov.PriceKWh, Currency: o.ov.Currency, CoinbaseTag: o.ov.CoinbaseTag,
			FanFixed: max(o.fanFixed, 0), FanTarget: o.target, UpdateCheck: !o.ov.NoUpdateCheck}
	}
	// save writes the levels, night mode and fan in force to nano3d's settings file.
	save := func() {
		lv := o.level
		x := settings()
		o.ov.Level, o.ov.Night, o.ov.NightLevel, o.ov.TZ = &lv, x.Night, x.NightLevel, x.TZ
		o.ov.FanFixed, o.ov.FanTarget = x.FanFixed, x.FanTarget
		if err := o.ov.Save(o.settings); err != nil {
			log.Printf("settings: %v", err)
		}
		if webs != nil {
			webs.SetSettings(x)
		}
	}
	if webs != nil {
		webs.SetSettings(settings())
	}
	feed := func() {}
	if o.watchdog {
		wd, err := board.OpenWatchdog()
		switch {
		case errors.Is(err, syscall.EBUSY):
			// btcminer starts wpa_supplicant and udhcpc without close-on-exec, so after a
			// stock btcminer ran they hold /dev/watchdog (seen live). That is the case in a
			// test session; at boot from slot A nothing holds it. btcminer also carries on
			// without a watchdog when its open fails.
			log.Printf("watchdog busy (inherited by the stock network daemons): running without it")
		case err != nil:
			return fmt.Errorf("watchdog: %w", err)
		default:
			defer wd.Close()
			feed = func() {
				if err := wd.Feed(); err != nil {
					log.Printf("watchdog feed: %v", err)
				}
			}
			feed()
			log.Printf("watchdog armed: 89 s")
		}
	}
	// Until the main loop runs, Wi-Fi and pool setup can take longer than the 89 s
	// timeout, so a helper feeds meanwhile; the main loop feeds by itself.
	stopFeeder := make(chan struct{})
	go func() {
		t := time.NewTicker(feedEvery)
		defer t.Stop()
		for {
			select {
			case <-stopFeeder:
				return
			case <-t.C:
				feed()
			}
		}
	}()
	feederRunning := true
	stopStartupFeeder := func() {
		if feederRunning {
			close(stopFeeder)
			feederRunning = false
		}
	}
	defer stopStartupFeeder()

	// Caught from here on, so a stop during the set-up still runs the deferred clean-up
	// (fan to 100 %, watchdog released) instead of killing the process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Release the K210 first, as btcminer does at start: on a cold boot it then has the
	// Wi-Fi and pool set-up time to boot before DETECT.
	if err := k210.EnableLine(); err != nil {
		return fmt.Errorf("K210 enable line: %w", err)
	}
	if o.wifi != nil {
		show("Wi-Fi", o.wifi.SSID)
		if err := o.wifi.BringUpStation(confiles, 2*time.Minute, nil); err != nil {
			return err
		}
	}

	// pc is nil while the pool is lost and a reconnect runs; the MM keeps its last job.
	show("pool", "connecting")
	pc, err := connect(o.pools, o.suggest, 2*time.Minute, sig)
	if errors.Is(err, errSignal) {
		log.Print(err)
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		if pc != nil {
			pc.Close()
		}
	}()

	f, err := k210.OpenUART(o.uart)
	if err != nil {
		return err
	}
	link := k210.NewLink(f)
	defer link.Close()
	// On the way out only the fan goes to 100 % (above); the MM keeps its level. Leaving
	// it at level 0 made a restart unsafe: the MM ramps its clocks slowly down, then up
	// again for the next nano3d, whose fan PID had followed the cooling chips down to
	// 33 % meanwhile, and the hottest ASIC reached 96 C (live).
	halted := false // K210 held in reset by the safety
	curLevel := o.levelAt(time.Now())
	info, err := link.BringUp(curLevel)
	if err != nil {
		return err
	}
	mineStart := time.Now() // the warm-up counts from here, not from the process start
	log.Printf("MM up: version %s, %s/%s, max level %d, level set to %d",
		info.Version, info.HWType, info.SWType, info.MaxLevel, curLevel)
	// Both scheduled levels, not only the one in force now: the other reaches
	// SetWorkLevel later. The web page checks the same range.
	for _, l := range []struct {
		name string
		v    int
	}{{"-level", o.level}, {"-night-level", o.nightLevel}} {
		if l.v < 0 || l.v >= info.MaxLevel {
			return fmt.Errorf("%s %d outside 0..%d (the MM reports max level %d)", l.name, l.v, info.MaxLevel-1, info.MaxLevel)
		}
	}

	var st stats
	book := &work.Book{}
	pending := map[uint64]string{}  // submit id -> job id, for the result log
	var redial chan *stratum.Client // set while a reconnect runs
	lastPoolMsg := time.Now()
	var primary chan *stratum.Client // set while the first pool is probed from a fallback
	lastPrimaryTry := time.Now()
	// drop closes a connection and lets its reader end.
	drop := func(old *stratum.Client) {
		old.Close()
		go func() {
			for range old.In {
			}
		}()
	}
	// lost drops the pool connection and reconnects in the background, through the
	// pool list as at start, until one answers with a job. Polling, fan, safety and
	// the watchdog go on meanwhile.
	lost := func(why error) {
		log.Printf("pool lost: %v; reconnecting", why)
		drop(pc)
		pc, pending = nil, map[uint64]string{}
		ch := make(chan *stratum.Client, 1)
		redial = ch
		pools := o.pools
		go func() {
			for {
				c, err := connect(pools, o.suggest, 2*time.Minute, nil)
				if err == nil {
					ch <- c
					return
				}
				log.Printf("reconnect: %v", err)
			}
		}()
	}
	send := func(newBlock bool) error {
		if pc == nil { // no pool: the MM gets work once the reconnect is done
			return nil
		}
		p := pc.Pool
		p.Tag = []byte(o.ov.CoinbaseTag) // the tag in force now: a change applies to the next job
		frames, j := work.Build(*pc.Notify, p, newBlock)
		for _, fr := range frames {
			if err := link.Send(fr); err != nil {
				return err
			}
		}
		book.Add(j, newBlock)
		log.Printf("job %s (tag %04x) diff=%g restart=%v", j.JobID, j.JC, j.PoolDiff, newBlock)
		return nil
	}
	defer func() {
		log.Printf("done: polls=%d status=%d crc_bad=%d records=%d unknown_job=%d valid=%d rewound=%d invalid=%d "+
			"meet_pool=%d submitted=%d accepted=%d rejected=%d best_zero_bits=%d",
			st.polls, st.status, st.crcBad, st.records, st.unknownJob, st.valid, st.rewound, st.invalid,
			st.poolOK, st.submitted, st.accepted, st.rejected, st.best)
	}()
	// The first burst always restarts the MM: it drops whatever work was queued before.
	if err := send(true); err != nil {
		return err
	}

	var end <-chan time.Time
	if o.seconds > 0 {
		end = time.After(time.Duration(o.seconds) * time.Second)
	}
	var ta uint32    // ASIC count from 0x51: the nonce2 step of the retry rule
	var ghsMM uint32 // MM-reported hashrate in MH/s (0x51 payload 0x09, "GHSmm")
	mmLevel := -1    // work level the MM reports (0x51 payload 0x30), -1 before the first
	var lastLevelSent time.Time
	asicTemps := temps{}
	// Power and health telemetry, for the web page and the LCD.
	var power k210.Power // latest valid 0x55 report
	var powerAt time.Time
	var energyWh float64 // input energy since start, from the 0x55 reports
	var extra k210.StatusExtra
	var freqs [4]int
	var levels []k210.Level
	asicVolts := map[uint16]float32{}
	asicErr := map[uint16]float64{}
	var lastSample time.Time
	mmTarget := 90 // btcminer sets 90 after detect; 0x51 then reports the MM's target
	var pid board.PID
	if fan != nil {
		pid = fan.SeedPID()
	}
	safety := &control.Safety{MaxTemp: float32(o.maxTemp), StaleAfter: time.Minute,
		HotToHalt: 2 * time.Minute, ClearFor: 5 * time.Minute, Cooldown: o.cooldown}
	lastPVT := time.Now() // a reading must arrive within StaleAfter of the start
	// applySafety acts on a safety state change; level and fan follow on every tick.
	applySafety := func(from, to control.State) error {
		switch {
		case to == control.Halted:
			halted = true
			return k210.HoldReset()
		case from == control.Halted && to == control.Hot:
			if err := k210.EnableLine(); err != nil {
				return err
			}
			var err error
			for try := 0; try < 5; try++ { // the K210 boots after the reset is released
				time.Sleep(2 * time.Second)
				if _, err = link.BringUp(0); err == nil {
					break
				}
			}
			if err != nil {
				return fmt.Errorf("K210 did not come back after the over-heat stop: %w", err)
			}
			halted, curLevel = false, 0
			mineStart, lastLevelSent = time.Now(), time.Now() // the MM settles again from here
			asicTemps, lastPVT = temps{}, time.Now()
			return send(true)
		}
		return nil
	}
	// The pool's view of this worker, read from a Public Pool API once a minute off the
	// main loop.
	type poolView struct {
		w   poolstats.Worker
		err error
	}
	views := make(chan poolView, 1)
	var view *poolstats.Worker
	var viewErr string
	var lastView time.Time
	// Release check and self-update. A goroutine asks GitHub for the latest release once
	// a day (and when the owner turns the check on), once NTP has set the clock, which
	// TLS needs. The download runs off the main loop too; the loop only keeps the state
	// for the page and exits once a verified binary waits in /tmp.
	found := make(chan update.Release, 1)
	checkNow := make(chan struct{}, 1)
	var checkOn atomic.Bool
	checkOn.Store(!o.ov.NoUpdateCheck)
	go func() {
		wait := time.Duration(0)
		for {
			select {
			case <-checkNow:
			case <-time.After(wait):
			}
			wait = updateEvery
			for time.Now().Year() < 2020 {
				time.Sleep(10 * time.Second)
			}
			if !checkOn.Load() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			r, err := update.Latest(ctx, ver)
			cancel()
			if err != nil {
				log.Printf("update check: %v", err)
				wait = time.Hour
				continue
			}
			select {
			case found <- r:
			default:
			}
		}
	}()
	var latest *update.Release
	var lastCheck, restartAt time.Time
	downloading, updateErr := false, ""
	var progress atomic.Int64 // percent of the binary downloaded
	downloaded := make(chan error, 1)
	updateInfo := func(now time.Time) web.Update {
		u := web.Update{Current: release, Progress: int(progress.Load()), Error: updateErr, CheckedS: -1}
		if latest != nil {
			u.Latest, u.URL, u.Newer = latest.Tag, latest.URL, update.Newer(release, latest.Tag)
			u.CheckedS = int64(now.Sub(lastCheck).Seconds())
		}
		switch {
		case !restartAt.IsZero():
			u.State = "restarting"
		case downloading:
			u.State = "downloading"
		case updateErr != "":
			u.State = "error"
		}
		return u
	}
	var presses <-chan struct{}
	if o.button {
		if b, err := board.OpenButton(); err != nil {
			log.Printf("button: %v: running without it", err)
		} else {
			defer b.Close()
			presses = board.Presses(b)
		}
	}
	poll := k210.PollFrame(false)
	wasFixed := false
	var lastMeasured time.Time
	stopStartupFeeder()
	var lastPoll time.Time
	lastFan, lastFeed, lastStatus := time.Now(), time.Now(), time.Now()
	for {
		select {
		case s := <-sig:
			log.Printf("signal %v: stopping", s)
			return nil
		case <-end:
			return nil
		case _, ok := <-presses:
			if !ok {
				presses = nil
				break
			}
			// The button cycles the level in force now through 0..max-1, as stock
			// button_detect_cb does; the next fan tick applies it.
			lv := &o.level
			if o.isNight(time.Now()) {
				lv = &o.nightLevel
			}
			*lv = (*lv + 1) % max(info.MaxLevel, 1)
			log.Printf("button: level %d", *lv)
			save()
		case x := <-changes: // validated by the web server
			o.level, o.nightLevel, o.night = x.Level, x.NightLevel, nil
			if w, err := control.ParseWindow(x.Night); err == nil {
				o.night = &w
			}
			if loc, err := time.LoadLocation(x.TZ); err == nil {
				o.loc = loc
			}
			o.ov.PriceKWh, o.ov.Currency = x.PriceKWh, x.Currency
			o.fanFixed = -1 // 0 on the page is the PID
			if x.FanFixed > 0 {
				o.fanFixed = x.FanFixed
			}
			o.target = x.FanTarget           // 0 on the page is the MM's target
			o.ov.CoinbaseTag = x.CoinbaseTag // in the next job the pool sends; the MM keeps
			// hashing the current coinbase, so resending it with a new tag would break its nonces
			if x.PoolAPI != o.ov.PoolAPI || x.PoolAPICA != o.ov.PoolAPICA {
				o.ov.PoolAPI, o.ov.PoolAPICA = x.PoolAPI, x.PoolAPICA
				view, viewErr, lastView = nil, "", time.Time{} // read it again at the next tick
			}
			newPools := !slices.Equal(x.Pools, o.pools)
			if newPools {
				o.pools, o.ov.Pools = x.Pools, x.Pools
			}
			if x.UpdateCheck && o.ov.NoUpdateCheck { // turned on: ask now, not in a day
				select {
				case checkNow <- struct{}{}:
				default:
				}
			}
			o.ov.NoUpdateCheck = !x.UpdateCheck
			checkOn.Store(x.UpdateCheck)
			save()
			log.Printf("web: settings saved (level %d, night %q level %d, tz %s, new pools %v)",
				o.level, x.Night, o.nightLevel, o.loc, newPools)
			if newPools {
				time.Sleep(time.Second) // let the web page get its answer first
				return errRestart
			}
		case r := <-found:
			if latest == nil || latest.Tag != r.Tag {
				log.Printf("update: latest release %s (running %q)", r.Tag, release)
			}
			latest, lastCheck = &r, time.Now()
		case <-updates: // the Update button on the dashboard
			if downloading || !restartAt.IsZero() {
				break
			}
			if latest == nil || !update.Newer(release, latest.Tag) {
				updateErr = "no newer release known"
				break
			}
			downloading, updateErr = true, ""
			progress.Store(0)
			r := *latest
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
				defer cancel()
				downloaded <- update.Download(ctx, r, ver, func(done, total int64) {
					if total > 0 {
						progress.Store(done * 100 / total)
					}
				})
			}()
			log.Printf("update: downloading %s", r.Tag)
		case err := <-downloaded:
			downloading = false
			if err != nil {
				updateErr = err.Error()
				log.Printf("update: %v", err)
				break
			}
			log.Printf("update: %s verified and staged in %s", latest.Tag, update.Staged)
			restartAt = time.Now().Add(3 * time.Second) // the page gets a status that says so first
		default:
		}

		now := time.Now()
		if !restartAt.IsZero() && now.After(restartAt) {
			return errUpdate
		}
		if now.Sub(lastFeed) >= feedEvery {
			feed() // fed from this loop only: a stalled loop lets the watchdog reboot the board
			lastFeed = now
		}
		if now.Sub(lastFan) >= fanEvery {
			mean, hottest := asicTemps.mean()
			prev := safety.State()
			state := safety.Update(now, hottest, lastPVT)
			if state != prev {
				log.Printf("safety: %v -> %v (hottest ASIC %.1f C, limit %.0f C)", prev, state, hottest, o.maxTemp)
				if err := applySafety(prev, state); err != nil {
					return err
				}
				// Leaving the safety, continue from the forced 100 % rather than from where
				// the PID drifted meanwhile (it falls to 25 % below 50 C: live, 100 -> 25 %).
				if state == control.OK && fan != nil {
					pid = board.PIDAt(fan.Permille())
				}
			}
			fixed := o.fanFixedAt(now)
			if fan != nil && fixed < 0 && wasFixed { // back to the PID: continue from the duty in force
				pid = board.PIDAt(fan.Permille())
			}
			wasFixed = fixed >= 0
			// level: the schedule, or 0 while the safety is not ok
			if want := o.levelAt(now); !halted {
				if state != control.OK {
					want = 0
				}
				if want != curLevel {
					// Going up from a quiet fan, the PID ramps too slowly: from 31 % the
					// hottest ASIC hit 96 C one minute after a 0 -> 2 switch (live). Start
					// at the measured full-power steady state instead and let the PID refine.
					if want > curLevel && fan != nil {
						pid = board.PIDAt(max(fan.Permille(), fullPowerDuty))
					}
					if err := link.SetWorkLevel(want); err != nil {
						return err
					}
					log.Printf("work level %d -> %d", curLevel, want)
					curLevel, lastLevelSent = want, now
				}
			}
			// SET_WORKLEVEL has no reply: if the MM missed one, its own report (0x51 payload
			// 0x30) keeps the old level, so send the wanted level again. The report changes
			// at once; the clocks follow slowly, about 8 MHz/s (2 -> 0 -> 2 took 110 s live).
			if !halted && mmLevel >= 0 && mmLevel != curLevel && now.Sub(lastLevelSent) >= 10*time.Second {
				log.Printf("MM reports level %d, want %d: sending it again", mmLevel, curLevel)
				if err := link.SetWorkLevel(curLevel); err != nil {
					return err
				}
				lastLevelSent = now
			}
			if fan != nil {
				// No reading yet (a hot restart, before the first 0x54): keep the duty in
				// force rather than the PID's cold 25 %. A minute without readings trips
				// the safety anyway.
				duty := fan.Permille()
				if mean >= 0 {
					duty = pid.Step(mean, o.fanTargetAt(now, mmTarget))
				}
				if fixed >= 0 { // a floor: the PID still raises the fan when the chips need it
					duty = max(duty, min(max(fixed, 10), 100)*10)
				}
				if state != control.OK {
					duty = 1000
				}
				if err := fan.Set(duty); err != nil {
					return fmt.Errorf("fan: %w", err)
				}
			}
			fanPct, poolAddr, poolUser, poolDiff := -1, "", "", 0.0
			if fan != nil {
				fanPct = fan.Permille() / 10
			}
			if pc != nil {
				poolAddr, poolUser, poolDiff = pc.Addr, pc.User, pc.Pool.Difficulty
			}
			select {
			case v := <-views:
				if v.err != nil {
					if v.err.Error() != viewErr {
						log.Printf("pool view: %v", v.err)
					}
					view, viewErr = nil, v.err.Error()
				} else {
					view, viewErr = &v.w, ""
				}
			default:
			}
			if o.ov.PoolAPI != "" && poolUser != "" && now.Sub(lastView) >= time.Minute {
				lastView = now
				go func(base, ca, user string) {
					w, err := poolstats.Fetch(base, ca, user)
					views <- poolView{w, err}
				}(o.ov.PoolAPI, o.ov.PoolAPICA, poolUser)
			}
			var pv screen.Status // only the pool-view fields are used
			if view != nil {
				pv = screen.Status{PoolView: true, PoolTHs: view.HashRate / 1e12, PoolBest: view.BestDiff}
			}
			ths := float64(ghsMM) / 1e6
			// The MM's expected hashrate (GHSspd) climbs for minutes after a start (0.6
			// TH/s at 1 minute, 4.0 at 5 minutes live): the level estimates built on it
			// wait for it to settle.
			settledSpeed := extra.SpeedMHs
			if time.Since(mineStart) < warmUp {
				settledSpeed = 0
			}
			powerW, inputA, jth := -1, 0.0, 0.0
			if now.Sub(powerAt) < time.Minute {
				powerW = power.Watts
				if power.InputV > 0 {
					inputA = float64(power.Watts) / power.InputV
				}
				if ths > 0 {
					jth = float64(power.Watts) / ths
				}
			}
			// Remember the hashrate measured at the level in force once it has run for
			// warmUp at a steady state (GHSmm is the MM's own average): the planner then
			// shows measured figures from the first second of the next start.
			if ths > 0 && state == control.OK && !halted && time.Since(mineStart) >= warmUp &&
				now.Sub(lastLevelSent) >= warmUp && now.Sub(lastMeasured) >= warmUp {
				lastMeasured = now
				for len(o.ov.LevelTHs) < info.MaxLevel {
					o.ov.LevelTHs = append(o.ov.LevelTHs, 0)
				}
				// Only a real change is worth a flash write (GHSmm wanders by about 1 %).
				// Written on its own: save() would also persist levels given on the
				// command line for a test session.
				if old := o.ov.LevelTHs[curLevel]; math.Abs(ths-old) > 0.05*ths {
					o.ov.LevelTHs[curLevel] = ths
					if err := o.ov.Save(o.settings); err != nil {
						log.Printf("settings: %v", err)
						o.ov.LevelTHs[curLevel] = old // so the next measurement tries again
					}
				}
			}
			if webs != nil {
				if now.Year() >= 2020 && now.Sub(lastSample) >= time.Minute { // not before NTP: a 1970 point flattens the charts
					webs.Record(web.Sample{T: now.Unix(), THs: ths, PowerW: powerW, TempMax: hottest,
						FanPct: fanPct, Level: curLevel})
					lastSample = now
				}
				webs.Publish(web.Status{Version: ver, UptimeS: int64(time.Since(start).Seconds()),
					Safety: state.String(), Level: curLevel, MaxLevel: info.MaxLevel, THs: ths,
					Night: o.isNight(now), WarmingUp: time.Since(mineStart) < warmUp,
					TempMean: mean, TempMax: hottest, ASICTemps: byASIC(asicTemps), TargetC: o.fanTargetAt(now, mmTarget), FanPct: fanPct, FanFixed: fixed >= 0,
					Pool: poolAddr, PoolUser: poolUser, PoolDiff: poolDiff, Valid: st.valid,
					Accepted: st.accepted, Rejected: st.rejected, Best: st.best, IP: wlanIP(),
					PoolView: o.ov.PoolAPI != "", PoolSeenTHs: pv.PoolTHs, PoolBestDiff: pv.PoolBest,
					PoolLastSeenS: lastSeen(view), PoolViewErr: viewErr,
					PowerW: powerW, InputV: power.InputV, InputA: inputA, JPerTH: jth, EnergyKWh: energyWh / 1000,
					PriceKWh: o.ov.PriceKWh, Currency: o.ov.Currency, Levels: levelEstimates(levels, freqs, settledSpeed, o.ov.LevelTHs),
					ExpectedTHs: float64(extra.SpeedMHs) / 1e6, HWErrPct: extra.DHPct, PLLMHz: slices.Clone(freqs[:]),
					ASICVolts: byASIC(asicVolts), ASICErrPct: byASIC(asicErr), Update: updateInfo(now)})
			}
			if scr != nil {
				var hist []float64
				if webs != nil {
					hist = webs.Hashrates()
				}
				scr.Draw(screen.Status{TH: ths, Expected: float64(extra.SpeedMHs) / 1e6, PowerW: powerW, JPerTH: jth,
					TempMean: mean, TempMax: hottest, Fan: fanPct, Level: curLevel, Night: o.isNight(now),
					Hot: state != control.OK, Safety: state.String(), Pool: poolAddr, Accepted: st.accepted,
					Rejected: st.rejected, Best: st.best, IP: wlanIP(), UpS: int64(time.Since(start).Seconds()),
					PoolView: pv.PoolView, PoolTHs: pv.PoolTHs, PoolBest: pv.PoolBest, Hist: hist})
			}
			lastFan = now
		}
		if now.Sub(lastStatus) >= statusEvery {
			mean, hottest := asicTemps.mean()
			fanPct := -1
			if fan != nil {
				fanPct = fan.Permille() / 10
			}
			log.Printf("status: level %d safety %v, %.2f TH/s, asic temp mean %.1f max %.1f target %d fan %d%% valid=%d accepted=%d rejected=%d",
				curLevel, safety.State(), float64(ghsMM)/1e6, mean, hottest, o.fanTargetAt(now, mmTarget), fanPct, st.valid, st.accepted, st.rejected)
			lastStatus = now
		}

		// Pace polls like btcminer (about every 110 ms live: 1600 polls in 180 s). The
		// MM answers in a few ms, and polling flat out only loads the single CPU core.
		if wait := pollEvery - time.Since(lastPoll); wait > 0 {
			time.Sleep(wait)
		}
		lastPoll = time.Now()
		var fr k210.Frame
		err := k210.ErrTimeout
		if !halted { // while the K210 is held in reset there is nothing to poll
			if err := link.Send(poll); err != nil {
				return err
			}
			st.polls++
			fr, err = link.Recv(300 * time.Millisecond)
		}
		switch {
		case err == k210.ErrCRC:
			st.crcBad++ // a corrupt frame is never parsed
		case err == k210.ErrTimeout:
		case err != nil:
			return err
		case fr.Type == k210.TypeStatus:
			st.status++
			p := fr.Payload
			if len(p) >= 0x25 {
				ta = binary.LittleEndian.Uint32(p[0x21:])
				ghsMM = binary.LittleEndian.Uint32(p[0x09:])
			}
			if x, ok := k210.ParseStatusExtra(p); ok {
				extra = x
			}
			if len(p) > 0x30 {
				mmLevel = int(p[0x30])
			}
			if len(p) > 0x31 && p[0x31] >= 50 && p[0x31] <= 100 { // range as 0xb5cc0
				mmTarget = int(p[0x31])
			}
			// btcminer answers 4 (overheat) and 7 (MM wants a reboot) with a reset
			// request on the next poll. Send it, then exit so the caller brings the MM up
			// again from scratch.
			if len(p) > 0 && (p[0] == 4 || p[0] == 7) {
				link.Send(k210.PollFrame(true))
				return mmResetError(p[0])
			}
		case fr.Type == k210.TypePVT:
			if fr.Idx >= maxASICs {
				break // a corrupt index would grow the per-ASIC lists for good
			}
			// per-ASIC temperature, 0.1 deg C, divided as an integer first (0x2d418)
			if len(fr.Payload) >= 2 {
				t := float32(binary.LittleEndian.Uint16(fr.Payload) / 10)
				asicTemps[fr.Idx] = t
				if t > 0 { // a zero is "no reading": it must not look like a fresh, cool one
					lastPVT = time.Now()
				}
			}
			if len(fr.Payload) >= 4 { // voltage x10, same integer division
				asicVolts[fr.Idx] = float32(binary.LittleEndian.Uint16(fr.Payload[2:]) / 10)
			}
		case fr.Type == k210.TypePower:
			if pw, ok := k210.ParsePower(fr.Payload); ok {
				now := time.Now()
				if dt := now.Sub(powerAt); dt < time.Minute { // a longer gap is not counted
					energyWh += float64(power.Watts) * dt.Hours()
				}
				power, powerAt = pw, now
			}
		case fr.Type == k210.TypeFreqs:
			if f, ok := k210.ParseFreqs(fr.Payload); ok {
				freqs = f
			}
		case fr.Type == k210.TypeCounters:
			if pct, ok := k210.ErrorPct(fr.Payload); ok && fr.Idx < maxASICs {
				asicErr[fr.Idx] = pct
			}
		case fr.Type == k210.TypeLevelParam:
			levels = k210.Levels(fr.Payload, info.MaxLevel)
		case fr.Type == k210.TypeNonce:
			recs, wrap, hasWrap := work.Records(fr.Payload)
			for _, r := range recs {
				st.records++
				s, rewound, ok := book.Check(r, ta, wrap, hasWrap)
				if s.Job == nil {
					st.unknownJob++ // a job from before nano3d started, or already dropped
					continue
				}
				if !ok {
					st.invalid++
					continue
				}
				st.valid++
				if rewound {
					st.rewound++
				}
				st.best = max(st.best, work.ZeroBits(s.Value))
				// pools judge a share by the difficulty in force when its job was sent
				if s.Value.Cmp(s.Job.Target) > 0 {
					continue
				}
				st.poolOK++
				if !o.submit || s.Job.Stale || pc == nil {
					continue
				}
				en2, ntime, nonce, ver := s.SubmitParams(pc.Pool.N2Size, pc.Pool.VMask != 0)
				id, err := pc.Submit(s.Job.JobID, en2, ntime, nonce, ver)
				if err != nil { // a dead connection: reconnect, as when the reader sees it
					lost(fmt.Errorf("submit: %w", err))
					continue
				}
				pending[id] = s.Job.JobID
				st.submitted++
				if len(pending) > maxPending { // a pool that never answers submits is as good as lost
					lost(fmt.Errorf("%d shares unanswered", len(pending)))
				}
			}
		}

		// Pool messages: submit results and new jobs. Every new job goes to the MM,
		// as btcminer does; only a new block restarts it (tested live, JOURNAL 20:39).
		newJob, newBlock := false, false
		if pc == nil {
			select {
			case c := <-redial:
				pc, redial, lastPoolMsg = c, nil, time.Now()
				// a new connection has a new extranonce1: restart the MM on its job,
				// which also marks every job of the old connection stale
				newJob, newBlock = true, true
			default:
			}
		}
		// Back to the first pool when it answers again (the probe runs off the loop).
		if pc != nil && len(o.pools) > 1 && (pc.Addr != o.pools[0].Addr || pc.User != o.pools[0].User) && primary == nil &&
			time.Since(lastPrimaryTry) >= primaryRetry {
			lastPrimaryTry = time.Now()
			ch := make(chan *stratum.Client, 1)
			primary = ch
			go func(p config.Pool) {
				c, err := connect([]config.Pool{p}, o.suggest, 0, nil) // one pass
				if err != nil {
					c = nil
				}
				ch <- c
			}(o.pools[0])
		}
		if primary != nil {
			select {
			case c := <-primary:
				primary = nil
				switch {
				case c == nil:
				case pc == nil: // lost meanwhile: the reconnect tries the first pool anyway
					drop(c)
				default:
					log.Printf("pool %s is back: leaving %s", c.Addr, pc.Addr)
					drop(pc)
					pc, pending, lastPoolMsg = c, map[uint64]string{}, time.Now()
					newJob, newBlock = true, true
				}
			default:
			}
		}
		// At most a batch per tick: a flood of messages must not hold off the polling,
		// the fan and the watchdog.
	pool:
		for n := 0; pc != nil && n < 64; n++ {
			select {
			case m, ok := <-pc.In:
				if !ok {
					lost(pc.Err())
					break pool
				}
				lastPoolMsg = time.Now()
				if id, isNum := m.IDNum(); isNum {
					if jid, mine := pending[id]; mine {
						delete(pending, id)
						if string(m.Result) == "true" {
							st.accepted++
						} else {
							st.rejected++
							log.Printf("share for job %s rejected: %s", jid, m.Error)
						}
						continue
					}
				}
				nj, clean := pc.Dispatch(m)
				newJob = newJob || nj
				newBlock = newBlock || (nj && clean)
			default:
				break pool
			}
		}
		if pc != nil && time.Since(lastPoolMsg) > poolSilence {
			lost(fmt.Errorf("no message for %s", poolSilence))
		}
		if newJob && !halted { // after a stop, the resume sends the latest job anyway
			if err := send(newBlock); err != nil {
				return err
			}
		}
	}
}
