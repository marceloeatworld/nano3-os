package stratum

import (
	"encoding/json"
	"testing"
)

func TestGrantedMask(t *testing.T) {
	for in, want := range map[string]uint32{
		"1fffe000": 0x1FFFE000,
		"ffffffff": 0x1FFFE000, // a superset is cut to what was requested
		"00000000": 0,          // nothing granted: no rolling
		"1fff0000": 0x1FFF0000,
		"zz":       0,
	} {
		if got := GrantedMask(in); got != want {
			t.Errorf("GrantedMask(%q) = %08x, want %08x", in, got, want)
		}
	}
}

func notify(params string) Msg {
	return Msg{Method: "mining.notify", Params: json.RawMessage(params)}
}

func TestDispatchNotify(t *testing.T) {
	c := &Client{}
	ok := `["1d7","` + zeros(64) + `","01","02",["` + zeros(64) + `"],"20000000","17034219","671e2f00",true]`
	nj, clean := c.Dispatch(notify(ok))
	if !nj || !clean || c.Notify.JobID != "1d7" || len(c.Notify.Merkles) != 1 || c.Notify.Version != 0x20000000 {
		t.Fatalf("new=%v clean=%v notify=%+v", nj, clean, c.Notify)
	}
	if nj, _ := c.Dispatch(notify(ok)); nj {
		t.Fatal("same job id reported as new")
	}
	// a malformed job (prevhash not 32 bytes) is ignored: the previous job stays
	bad := `["1d8","00","01","02",[],"20000000","17034219","671e2f00",false]`
	if nj, _ := c.Dispatch(notify(bad)); nj || c.Notify.JobID != "1d7" {
		t.Fatalf("malformed notify accepted: %+v", c.Notify)
	}
}

func TestDispatchDifficultyAndMask(t *testing.T) {
	c := &Client{}
	c.Dispatch(Msg{Method: "mining.set_difficulty", Params: json.RawMessage(`[2048]`)})
	c.Dispatch(Msg{Method: "mining.set_version_mask", Params: json.RawMessage(`["ffffffff"]`)})
	if c.Pool.Difficulty != 2048 || c.Pool.VMask != 0x1FFFE000 {
		t.Fatalf("diff %g mask %08x", c.Pool.Difficulty, c.Pool.VMask)
	}
	c.Dispatch(Msg{Method: "mining.set_difficulty", Params: json.RawMessage(`[0]`)})
	if c.Pool.Difficulty != 2048 {
		t.Fatal("difficulty 0 accepted")
	}
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}
