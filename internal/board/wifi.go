package board

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// WiFi is the station setup from the stock config ([netcfg] in systemcfg.ini).
type WiFi struct {
	SSID     string
	Password string
	Auth     int // stock auth code: 1-3 open, 5 or 7 WPA-PSK (7 = WPA2-PSK, the default)
}

// wifiLock serializes the Wi-Fi set-up with the guard script.
const wifiLock = "/tmp/nano3-os-wifi.lock"

// Cmd is one command of the bring-up, run without a shell.
type Cmd struct {
	Args       []string
	IgnoreFail bool
	Sleep      time.Duration // pause after the command
}

// Label names a command for logs and errors without its values (no SSID, no key).
func (c Cmd) Label() string {
	if c.Args[0] != "wpa_cli" || len(c.Args) < 4 {
		return strings.Join(c.Args[:min(len(c.Args), 3)], " ")
	}
	if c.Args[3] == "set_network" && len(c.Args) > 5 {
		return "wpa_cli set_network " + c.Args[5]
	}
	return "wpa_cli " + c.Args[3]
}

// keyMgmt maps the stock auth code to wpa_supplicant's key_mgmt, as netdev_connect
// (0xbf530) does. EAP modes (4, 6, 8) are not supported by nano3d yet.
func keyMgmt(auth int) (string, error) {
	switch auth {
	case 1, 2, 3:
		return "NONE", nil
	case 5, 7:
		return "WPA-PSK", nil
	}
	return "", fmt.Errorf("wifi auth mode %d not supported (EAP)", auth)
}

// StationCommands returns btcminer's STA bring-up (netdev_deinit 0xbf300, netdev_init,
// netdev_connect 0xbf530): stop everything, start wpa_supplicant, add the network
// with wpa_cli, then DHCP. "NET" stands for the network id add_network returns.
func (w WiFi) StationCommands(confDir string) ([]Cmd, error) {
	if w.SSID == "" {
		return nil, errors.New("no wifi ssid in the stock config (AP setup mode is not supported by nano3d)")
	}
	km, err := keyMgmt(w.Auth)
	if err != nil {
		return nil, err
	}
	cli := func(a ...string) Cmd { return Cmd{Args: append([]string{"wpa_cli", "-i", "wlan0"}, a...)} }
	cmds := []Cmd{
		{Args: []string{"killall", "udhcpd"}, IgnoreFail: true},
		{Args: []string{"killall", "hostapd"}, IgnoreFail: true},
		{Args: []string{"ifconfig", "wlan1", "down"}, IgnoreFail: true},
		{Args: []string{"killall", "udhcpc"}, IgnoreFail: true},
		{Args: []string{"killall", "wpa_supplicant"}, IgnoreFail: true},
		{Args: []string{"ifconfig", "wlan0", "down"}, IgnoreFail: true, Sleep: time.Second},
		{Args: []string{"ifconfig", "wlan0", "up"}},
		{Args: []string{"wpa_supplicant", "-B", "-D", "nl80211", "-i", "wlan0", "-c", confDir + "/wpa_supplicant.conf"}, Sleep: time.Second},
		cli("add_network"),
		cli("set_network", "NET", "ssid", `"`+w.SSID+`"`),
		cli("set_network", "NET", "key_mgmt", km),
	}
	if km == "WPA-PSK" {
		cmds = append(cmds, cli("set_network", "NET", "psk", `"`+w.Password+`"`))
	}
	return append(cmds, cli("select_network", "NET"), Cmd{Args: []string{"udhcpc", "-i", "wlan0", "-b"}}), nil
}

// BringUpStation runs the station commands and waits until wpa_supplicant reports
// COMPLETED and DHCP gave an address, all within timeout. feed is called every second
// while waiting, as btcminer feeds its watchdog in the same loop.
func (w WiFi) BringUpStation(confDir string, timeout time.Duration, feed func()) error {
	cmds, err := w.StationCommands(confDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// One Wi-Fi set-up at a time: the root-filesystem guard (tools/nano3-os-guard.sh)
	// takes the same lock before it restarts the supplicant on its own.
	lock, err := os.OpenFile(wifiLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("wifi: %w", err)
	}
	defer lock.Close()
	for { // without blocking past the deadline: the guard may be mid set-up
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK {
			return fmt.Errorf("wifi: lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return errors.New("wifi: another set-up held the lock for the whole timeout")
		case <-time.After(time.Second):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	netID := ""
	for _, c := range cmds {
		args := append([]string(nil), c.Args...)
		for k, a := range args {
			if a == "NET" {
				args[k] = netID
			}
		}
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
		reply := strings.TrimSpace(string(out))
		switch {
		case err != nil && !c.IgnoreFail:
			return fmt.Errorf("wifi: %s: %v", c.Label(), err)
		case c.Label() == "wpa_cli add_network":
			if reply == "" || strings.Contains(reply, "FAIL") {
				return fmt.Errorf("wifi: add_network answered %q", reply)
			}
			netID = reply
		case strings.HasPrefix(c.Label(), "wpa_cli ") && reply != "OK" && c.Label() != "wpa_cli add_network":
			return fmt.Errorf("wifi: %s answered %q", c.Label(), reply)
		}
		time.Sleep(c.Sleep)
	}
	log.Printf("wifi: connecting to %q", w.SSID)
	for ctx.Err() == nil {
		if feed != nil {
			feed()
		}
		out, _ := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "status").Output()
		st := string(out)
		if strings.Contains(st, "wpa_state=COMPLETED") && strings.Contains(st, "ip_address=") {
			log.Printf("wifi: connected")
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("wifi: not connected to %q after %s", w.SSID, timeout)
}
