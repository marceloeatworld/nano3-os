package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The files follow the stock layout (key padded with spaces, "=" and the value).
const cgminerINI = `[cgminercfg]
url0                           = stratum+tcp://pool.example:3333
user0                          = bc1qexample.worker
pass0                          =
url1                           = stratum+tcp://backup.example:3334
user1                          = bc1qexample.worker
pass1                          = x
url2                           =
standard                       = --lowmem --real-quiet
`

const systemcfgINI = `[syscfg]
work_level                     = 2
ledmode                        = 1

[netcfg]
net_proto                      = 0
ssid                           = home net
password                       = p=ss word
auth                           = 7
timezone                       =
`

func TestLoadStock(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "cgminer.ini"), []byte(cgminerINI), 0o644)
	os.WriteFile(filepath.Join(dir, "systemcfg.ini"), []byte(systemcfgINI), 0o644)
	st, err := LoadStock(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Pool{
		{Addr: "pool.example:3333", User: "bc1qexample.worker", Pass: ""},
		{Addr: "backup.example:3334", User: "bc1qexample.worker", Pass: "x"},
	}
	if st.Net != (Net{SSID: "home net", Password: "p=ss word", Auth: 7}) {
		t.Fatalf("net %+v", st.Net)
	}
	if len(st.Pools) != 2 || st.Pools[0] != want[0] || st.Pools[1] != want[1] || st.WorkLevel != 2 {
		t.Fatalf("got %+v", st)
	}
}

func TestPoolAddrRejectsOtherSchemes(t *testing.T) {
	for _, u := range []string{"stratum+ssl://pool:3333", "pool-without-port", "http://pool:80"} {
		if _, err := PoolAddr(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
}
