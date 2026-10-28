package board

import (
	"strings"
	"testing"
)

func TestPIDBoundsAndOverrides(t *testing.T) {
	var p PID
	if got := p.Step(-273, 90); got != 250 { // no reading yet
		t.Fatalf("no data: %d, want 250", got)
	}
	if got := p.Step(101, 90); got != 1000 {
		t.Fatalf("over 100 C: %d, want 1000", got)
	}
	if p.acc != 1023 {
		t.Fatalf("acc %d after over-temperature, want 1023", p.acc)
	}
}

func TestPIDStepLimits(t *testing.T) {
	// A 10 degree jump above target gives 35*10 + 10 + 10 = 370, cut to +50.
	p := PID{acc: 500}
	got := p.Step(100, 90)
	if p.acc != 550 || got != 1000*550/1023 {
		t.Fatalf("acc %d permille %d", p.acc, got)
	}
	// Dropping back to target: 35*(0-10) + 0 + (0-20+0) = -370, cut to -100.
	got = p.Step(90, 90)
	if p.acc != 450 || got != 1000*450/1023 {
		t.Fatalf("acc %d permille %d", p.acc, got)
	}
}

func TestPIDHalfDegreeErrorAndFloor(t *testing.T) {
	// e = trunc(2*(90.7-90))/2 = 0.5 -> delta = round(35*0.5 + 0.5 + 0.5) = 19
	p := PID{acc: 300}
	p.Step(90.7, 90)
	if p.acc != 319 {
		t.Fatalf("acc %d, want 319", p.acc)
	}
	// a large negative step below 256 floors at 256 / 250 per mille
	p = PID{acc: 300, e1: 5}
	if got := p.Step(90, 90); got != 250 || p.acc != 256 {
		t.Fatalf("floor: permille %d acc %d", got, p.acc)
	}
}

func TestStationCommandsNeverLogSecrets(t *testing.T) {
	w := WiFi{SSID: "home net", Password: "s3cr3t pass;rm -rf", Auth: 7}
	cmds, err := w.StationCommands("/mnt/heater/confiles")
	if err != nil {
		t.Fatal(err)
	}
	var sawPSK bool
	for _, c := range cmds {
		if strings.Contains(c.Label(), "s3cr3t") || strings.Contains(c.Label(), "home net") {
			t.Fatalf("label leaks a value: %q", c.Label())
		}
		if c.Label() == "wpa_cli set_network psk" {
			sawPSK = true
			// the password is one argv element, quoted for wpa_cli, never parsed by a shell
			if c.Args[len(c.Args)-1] != `"s3cr3t pass;rm -rf"` {
				t.Fatalf("psk arg %q", c.Args[len(c.Args)-1])
			}
		}
	}
	if !sawPSK {
		t.Fatal("no psk step for WPA2-PSK")
	}
	if _, err := (WiFi{SSID: "x", Auth: 6}).StationCommands("/c"); err == nil {
		t.Fatal("EAP accepted")
	}
	if _, err := (WiFi{Auth: 7}).StationCommands("/c"); err == nil {
		t.Fatal("empty ssid accepted")
	}
}

func TestSeedPIDContinuesFromCurrentDuty(t *testing.T) {
	f := &Fan{last: 720} // 72 %, as a previous miner left it
	p := f.SeedPID()
	// at target with no history the step is 0: the duty stays where it was
	if got := p.Step(90, 90); got != 1000*736/1023 || p.acc != 736 {
		t.Fatalf("permille %d acc %d", got, p.acc)
	}
}

func TestPIDAtStartsAtGivenDuty(t *testing.T) {
	p := PIDAt(720)
	if got := p.Step(90, 90); got != 1000*736/1023 {
		t.Fatalf("permille %d, want about 720", got)
	}
}
