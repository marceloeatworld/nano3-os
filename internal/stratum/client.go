// Package stratum is a minimal Stratum V1 client: configure (BIP310 version rolling),
// subscribe, authorize, suggest_difficulty, notify, set_difficulty, set_version_mask
// and submit. It is a port of the client in tools/nano3miner.py.
package stratum

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"nano3-os/internal/work"
)

// RequestedMask is the version-rolling mask asked for in mining.configure.
const RequestedMask = 0x1FFFE000

// Largest job accepted from a pool (see parseNotify).
const (
	maxCoinbase = 16 << 10
	maxMerkles  = 64
)

// Msg is one message from the pool.
type Msg struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// IDNum returns the numeric id of a response, or false.
func (m Msg) IDNum() (uint64, bool) {
	n, err := strconv.ParseUint(string(m.ID), 10, 64)
	return n, err == nil
}

// Client is one pool connection.
type Client struct {
	conn   net.Conn
	mu     sync.Mutex // serializes writes
	nextID uint64
	User   string // the worker, as authorized
	pass   string

	Addr string // host:port, as dialed

	// In delivers every message from the pool; it is closed when the connection ends,
	// and Err then says why.
	In        <-chan Msg
	err       error
	done      chan struct{} // closed by Close: frees a reader blocked on a full In
	closeOnce sync.Once

	Pool   work.Pool
	Notify *work.Template // latest job
}

// Dial connects to host:port.
func Dial(addr, user, pass string) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	in := make(chan Msg, 64)
	c := &Client{conn: conn, User: user, pass: pass, Addr: addr, In: in, Pool: work.Pool{Difficulty: 1},
		done: make(chan struct{})}
	go c.read(in)
	return c, nil
}

func (c *Client) read(in chan<- Msg) {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64*1024), 1<<20) // a notify is a few kB; cap a runaway line at 1 MiB
scan:
	for sc.Scan() {
		var m Msg
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // not JSON: ignore the line
		}
		select {
		case in <- m:
		case <-c.done:
			break scan
		}
	}
	c.err = sc.Err()
	if c.err == nil {
		c.err = errors.New("pool closed the connection")
	}
	close(in)
}

// Err is why In was closed.
func (c *Client) Err() error { return c.err }

// Close ends the connection.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return c.conn.Close()
}

func (c *Client) call(method string, params any) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	b, err := json.Marshal(map[string]any{"id": c.nextID, "method": method, "params": params})
	if err != nil {
		return 0, err
	}
	// A peer that stops reading must not stall the caller, which is the main loop.
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err = c.conn.Write(append(b, '\n'))
	return c.nextID, err
}

// Handshake configures version rolling, subscribes and authorizes. It returns once
// the subscribe and authorize results arrived, dispatching anything that came before
// them. The configure result is not waited for: some pools ignore unknown methods.
func (c *Client) Handshake(suggestDiff float64, timeout time.Duration) error {
	cid, err := c.call("mining.configure", []any{[]string{"version-rolling"},
		map[string]string{"version-rolling.mask": fmt.Sprintf("%08x", RequestedMask)}})
	if err != nil {
		return err
	}
	sid, _ := c.call("mining.subscribe", []string{"nano3-os/0.1"})
	aid, err := c.call("mining.authorize", []string{c.User, c.pass})
	if err != nil {
		return err
	}
	if suggestDiff > 0 {
		c.call("mining.suggest_difficulty", []float64{suggestDiff})
	}
	deadline := time.After(timeout)
	subscribed, authorized := false, false
	for !(subscribed && authorized) {
		select {
		case m, ok := <-c.In:
			if !ok {
				return c.err
			}
			c.Dispatch(m)
			id, _ := m.IDNum()
			switch id {
			case cid:
				var r map[string]any
				if json.Unmarshal(m.Result, &r) == nil && r["version-rolling"] == true {
					if s, ok := r["version-rolling.mask"].(string); ok {
						c.Pool.VMask = GrantedMask(s)
					}
				}
			case sid:
				var r []json.RawMessage
				if json.Unmarshal(m.Result, &r) != nil || len(r) < 3 {
					return fmt.Errorf("bad subscribe result: %s", m.Result)
				}
				var en1 string
				json.Unmarshal(r[1], &en1)
				if c.Pool.ExtraNonce1, err = hex.DecodeString(en1); err != nil || len(c.Pool.ExtraNonce1) > 32 {
					return fmt.Errorf("bad extranonce1 %q", en1) // 4 to 8 bytes in practice; it goes in every coinbase
				}
				if json.Unmarshal(r[2], &c.Pool.N2Size) != nil || c.Pool.N2Size < 2 || c.Pool.N2Size > 16 {
					return fmt.Errorf("bad extranonce2 size %s", r[2])
				}
				subscribed = true
			case aid:
				var okAuth bool
				if json.Unmarshal(m.Result, &okAuth) != nil || !okAuth {
					return fmt.Errorf("authorize refused: %s %s", m.Result, m.Error)
				}
				authorized = true
			}
		case <-deadline:
			return errors.New("pool handshake timed out")
		}
	}
	return nil
}

// GrantedMask keeps only the bits inside the requested mask (BIP310).
func GrantedMask(hexMask string) uint32 {
	v, err := strconv.ParseUint(hexMask, 16, 32)
	if err != nil {
		return 0
	}
	return uint32(v) & RequestedMask
}

// Dispatch applies a pool notification. It reports a new job (a notify with a new
// job id) and whether that job is clean, i.e. a new block.
func (c *Client) Dispatch(m Msg) (newJob, clean bool) {
	switch m.Method {
	case "mining.set_difficulty":
		var p []float64
		if json.Unmarshal(m.Params, &p) == nil && len(p) > 0 && p[0] > 0 {
			c.Pool.Difficulty = p[0]
		}
	case "mining.set_version_mask":
		var p []string
		if json.Unmarshal(m.Params, &p) == nil && len(p) > 0 {
			c.Pool.VMask = GrantedMask(p[0])
		}
	case "mining.notify":
		t, err := parseNotify(m.Params)
		if err != nil {
			return false, false // malformed job: keep mining the previous one
		}
		t.Diff = c.Pool.Difficulty
		newJob = c.Notify == nil || c.Notify.JobID != t.JobID
		c.Notify = &t
		return newJob, t.Clean
	}
	return false, false
}

func parseNotify(raw json.RawMessage) (work.Template, error) {
	var p []json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil || len(p) < 9 {
		return work.Template{}, errors.New("bad notify")
	}
	var t work.Template
	var prev, cb1, cb2, ver, bits, ntime string
	var merkles []string
	for i, dst := range []any{&t.JobID, &prev, &cb1, &cb2, &merkles, &ver, &bits, &ntime, &t.Clean} {
		if err := json.Unmarshal(p[i], dst); err != nil {
			return work.Template{}, fmt.Errorf("bad notify field %d", i)
		}
	}
	var err error
	if t.PrevHash, err = hex.DecodeString(prev); err != nil || len(t.PrevHash) != 32 {
		return work.Template{}, errors.New("bad prevhash")
	}
	if t.Coinb1, err = hex.DecodeString(cb1); err != nil {
		return work.Template{}, err
	}
	if t.Coinb2, err = hex.DecodeString(cb2); err != nil {
		return work.Template{}, err
	}
	// Real coinbases are under 1 kB and merkle branches number about 13; a job far
	// beyond that would hold the UART and the main loop for seconds.
	if len(t.Coinb1)+len(t.Coinb2) > maxCoinbase || len(merkles) > maxMerkles {
		return work.Template{}, fmt.Errorf("job too large: coinbase %d bytes, %d merkle branches",
			len(t.Coinb1)+len(t.Coinb2), len(merkles))
	}
	for _, h := range merkles {
		b, err := hex.DecodeString(h)
		if err != nil || len(b) != 32 {
			return work.Template{}, errors.New("bad merkle branch")
		}
		t.Merkles = append(t.Merkles, b)
	}
	for _, f := range []struct {
		s   string
		dst *uint32
	}{{ver, &t.Version}, {bits, &t.NBits}, {ntime, &t.NTime}} {
		v, err := strconv.ParseUint(f.s, 16, 32)
		if err != nil {
			return work.Template{}, fmt.Errorf("bad hex field %q", f.s)
		}
		*f.dst = uint32(v)
	}
	return t, nil
}

// Submit sends a share and returns the request id its result will carry.
func (c *Client) Submit(jobID, en2, ntime, nonce, version string) (uint64, error) {
	params := []string{c.User, jobID, en2, ntime, nonce}
	if version != "" {
		params = append(params, version)
	}
	return c.call("mining.submit", params)
}
