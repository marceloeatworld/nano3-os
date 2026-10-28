// Package config reads the stock Avalon configuration so nano3d mines with the
// owner's existing settings: pools from /data/usrcon/cgminer.ini (url0..2, user0..2,
// pass0..2) and the work level from /data/usrcon/systemcfg.ini. It only reads them;
// the stock web UI stays the place to change them.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Pool is one configured pool.
type Pool struct {
	Addr string `json:"addr"` // host:port
	User string `json:"user"`
	Pass string `json:"pass"`
}

// Net is the Wi-Fi station setup from [netcfg] in systemcfg.ini.
type Net struct {
	SSID     string
	Password string
	Auth     int // stock auth code (7 = WPA2-PSK)
	Proto    int // 0 = DHCP; anything else is a static setup nano3d does not support
}

// Stock is what nano3d takes from the stock configuration.
type Stock struct {
	Pools     []Pool
	WorkLevel int // -1 when not set
	Net       Net
}

// parseINI returns key = value pairs by section. Keys are as written.
func parseINI(path string) (map[string]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = line[1 : len(line)-1]
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			out[section][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, sc.Err()
}

// PoolAddr turns a stock pool URL ("stratum+tcp://host:port") into host:port.
func PoolAddr(url string) (string, error) {
	addr := strings.TrimPrefix(url, "stratum+tcp://")
	if strings.Contains(addr, "://") || !strings.Contains(addr, ":") {
		return "", fmt.Errorf("unsupported pool URL %q (need stratum+tcp://host:port)", url)
	}
	return addr, nil
}

// LoadStock reads cgminer.ini and systemcfg.ini from dir (normally /data/usrcon).
func LoadStock(dir string) (Stock, error) {
	st := Stock{WorkLevel: -1}
	cg, err := parseINI(filepath.Join(dir, "cgminer.ini"))
	if err != nil {
		return st, err
	}
	sec := cg["cgminercfg"]
	for i := 0; i < 3; i++ {
		url := sec[fmt.Sprintf("url%d", i)]
		if url == "" {
			continue
		}
		addr, err := PoolAddr(url)
		if err != nil {
			return st, err
		}
		st.Pools = append(st.Pools, Pool{Addr: addr, User: sec[fmt.Sprintf("user%d", i)], Pass: sec[fmt.Sprintf("pass%d", i)]})
	}
	if len(st.Pools) == 0 {
		return st, fmt.Errorf("no pool in %s", filepath.Join(dir, "cgminer.ini"))
	}
	if sys, err := parseINI(filepath.Join(dir, "systemcfg.ini")); err == nil {
		if v, err := strconv.Atoi(sys["syscfg"]["work_level"]); err == nil {
			st.WorkLevel = v
		}
		n := sys["netcfg"]
		st.Net = Net{SSID: n["ssid"], Password: n["password"], Auth: 7}
		if v, err := strconv.Atoi(n["auth"]); err == nil {
			st.Net.Auth = v
		}
		if v, err := strconv.Atoi(n["net_proto"]); err == nil {
			st.Net.Proto = v
		}
	}
	return st, nil
}
