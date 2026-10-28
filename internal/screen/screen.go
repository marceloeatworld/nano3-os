// Package screen draws nano3d's status on the Nano 3's front LCD: an ST7789V,
// 240x240, that the kernel exposes as the framebuffer /dev/fb0 (fb_st7789v, RGB565,
// rotation done by the driver). The stock btcminer draws it with LVGL; nano3d writes
// whole frames, in the style of its web dashboard. Render gives the same frame as an
// image, for the lcdshot tool.
package screen

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// The dashboard palette (internal/web/index.html, dark scheme).
var (
	bg     = color.RGBA{0x11, 0x12, 0x17, 255}
	panel  = color.RGBA{0x18, 0x1b, 0x1f, 255}
	line   = color.RGBA{0x2c, 0x32, 0x35, 255}
	fg     = color.RGBA{0xd8, 0xd9, 0xdf, 255}
	dim    = color.RGBA{0x9f, 0xa7, 0xb3, 255}
	accent = color.RGBA{0x57, 0x94, 0xf2, 255}
	fill   = color.RGBA{0x24, 0x3a, 0x60, 255} // the accent at the area's opacity over bg
	green  = color.RGBA{0x73, 0xbf, 0x69, 255}
	yellow = color.RGBA{0xfa, 0xde, 0x2a, 255}
	red    = color.RGBA{0xf2, 0x49, 0x5c, 255}
)

// Status is what the status frame shows. The pool's address is never drawn: a photo
// of the LCD shows nothing but the miner's LAN address.
type Status struct {
	TH                 float64 // MM-reported hashrate, TH/s
	Expected           float64 // the MM's expected hashrate, 0 when unknown
	PowerW             int     // input power, -1 when unknown
	JPerTH             float64 // power / hashrate, 0 when unknown
	TempMean, TempMax  float32 // ASIC temperatures, deg C; mean < 0 when none yet
	Fan                int     // percent, -1 when nano3d does not drive the fan
	Level              int
	Night              bool   // the night window is in force
	Hot                bool   // over-heat safety active
	Safety             string // safety state, shown when Hot
	Pool               string // host:port, "" while the pool is lost (only "lost" is shown)
	Accepted, Rejected int
	Best               int // best share so far, in leading zero bits
	IP                 string
	UpS                int64 // seconds since nano3d started
	// The pool's view of this worker, when known.
	PoolView          bool
	PoolTHs, PoolBest float64
	Hist              []float64 // hashrate samples, oldest first (the 24 h chart); 0 = unknown
}

// si writes a share difficulty the way pool dashboards do: 419.6k, 24.4M.
func si(v float64) string {
	for _, u := range []string{"", "k", "M", "G", "T", "P"} {
		if v < 1000 {
			return fmt.Sprintf("%.3g%s", v, u)
		}
		v /= 1000
	}
	return fmt.Sprintf("%.3gE", v)
}

// dur writes an uptime as 3h12m or 2d 5h.
func dur(s int64) string {
	d, h, m := s/86400, s%86400/3600, s%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func sysInt(dir, name string) ([]int, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var out []int
	for _, f := range strings.Split(strings.TrimSpace(string(b)), ",") {
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %q", dir, name, b)
		}
		out = append(out, v)
	}
	return out, nil
}

func face(ttf []byte, size float64) (font.Face, error) {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// painter draws frames into an RGBA image.
type painter struct {
	img                   *image.RGBA
	big, mid, small, tiny font.Face
}

func newPainter(w, h int) (*painter, error) {
	p := &painter{img: image.NewRGBA(image.Rect(0, 0, w, h))}
	for _, x := range []struct {
		dst  *font.Face
		ttf  []byte
		size float64
	}{{&p.big, gobold.TTF, 54}, {&p.mid, gobold.TTF, 20}, {&p.small, goregular.TTF, 16}, {&p.tiny, goregular.TTF, 13}} {
		var err error
		if *x.dst, err = face(x.ttf, x.size); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Screen is an open framebuffer.
type Screen struct {
	*painter
	f      *os.File
	stride int
	buf    []byte
}

// Open opens a 16-bit framebuffer such as /dev/fb0, reading its geometry from sysfs.
func Open(dev string) (*Screen, error) {
	sys := filepath.Join("/sys/class/graphics", filepath.Base(dev))
	size, err := sysInt(sys, "virtual_size")
	if err != nil {
		return nil, err
	}
	bpp, err := sysInt(sys, "bits_per_pixel")
	if err != nil {
		return nil, err
	}
	stride, err := sysInt(sys, "stride")
	if err != nil {
		return nil, err
	}
	if len(size) != 2 || bpp[0] != 16 {
		return nil, fmt.Errorf("%s: want a 16-bit framebuffer, have size %v, %d bpp", dev, size, bpp[0])
	}
	p, err := newPainter(size[0], size[1])
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(dev, os.O_WRONLY, 0)
	if err != nil {
		return nil, err
	}
	return &Screen{painter: p, f: f, stride: stride[0], buf: make([]byte, stride[0]*size[1])}, nil
}

// Close releases the framebuffer; the last frame stays on the LCD.
func (s *Screen) Close() error { return s.f.Close() }

// flush converts the frame to RGB565 (little endian) and writes it.
func (s *Screen) flush() error {
	b := s.img.Rect
	for y := 0; y < b.Dy(); y++ {
		row := s.buf[y*s.stride:]
		for x := 0; x < b.Dx(); x++ {
			i := s.img.PixOffset(x, y)
			p := s.img.Pix[i : i+3]
			v := uint16(p[0])>>3<<11 | uint16(p[1])>>2<<5 | uint16(p[2])>>3
			row[2*x], row[2*x+1] = byte(v), byte(v>>8)
		}
	}
	_, err := s.f.WriteAt(s.buf, 0)
	return err
}

// Message shows a one-step screen, for start-up and stop: a title and a detail line.
func (s *Screen) Message(title, detail string, alert bool) error {
	s.message(title, detail, alert)
	return s.flush()
}

// Draw shows the mining status.
func (s *Screen) Draw(st Status) error {
	s.status(st)
	return s.flush()
}

// Render draws the status frame at the LCD's size and returns it, for previews.
func Render(st Status) (*image.RGBA, error) {
	p, err := newPainter(240, 240)
	if err != nil {
		return nil, err
	}
	p.status(st)
	return p.img, nil
}

func (p *painter) rect(x0, y0, x1, y1 int, c color.Color) {
	draw.Draw(p.img, image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{}, draw.Src)
}

func (p *painter) disc(cx, cy, r int, c color.Color) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y <= r*r {
				p.img.Set(cx+x, cy+y, c)
			}
		}
	}
}

func (p *painter) text(f font.Face, c color.Color, x, y int, str string) int {
	d := font.Drawer{Dst: p.img, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(str)
	return d.Dot.X.Round()
}

func width(f font.Face, str string) int { return font.MeasureString(f, str).Round() }

// fit shortens str with an ellipsis until it is at most w pixels wide in f.
func fit(f font.Face, str string, w int) string {
	r := []rune(str)
	for len(r) > 1 && width(f, string(r)) > w {
		r = append(r[:len(r)-2], '…')
	}
	return string(r)
}

func (p *painter) right(f font.Face, c color.Color, y int, str string) {
	p.text(f, c, p.img.Rect.Dx()-10-width(f, str), y, str)
}

func (p *painter) center(f font.Face, c color.Color, y int, str string) {
	p.text(f, c, (p.img.Rect.Dx()-width(f, str))/2, y, str)
}

// header draws the top bar: a status dot, the name, and a right-hand label.
func (p *painter) header(dot color.Color, right string) {
	w := p.img.Rect.Dx()
	p.rect(0, 0, w, p.img.Rect.Dy(), bg)
	p.rect(0, 0, w, 30, panel)
	p.rect(0, 30, w, 31, line)
	p.disc(13, 15, 4, dot)
	p.text(p.mid, fg, 24, 22, "nano3-os")
	if right != "" {
		p.right(p.small, dim, 21, right)
	}
}

func (p *painter) message(title, detail string, alert bool) {
	c := fg
	if alert {
		c = red
	}
	p.header(dim, "")
	p.center(p.mid, c, 122, title)
	p.center(p.small, dim, 150, detail)
}

// tile draws a label over a value, the value in c; x is the left edge.
func (p *painter) tile(x, y, w int, label, value string, c color.Color) {
	p.rect(x, y, x+w, y+44, panel)
	p.text(p.tiny, dim, x+6, y+15, label)
	p.text(p.mid, c, x+6, y+37, fit(p.mid, value, w-10))
}

// status is the mining frame, laid out for 240x240.
func (p *painter) status(st Status) {
	dot := green
	if st.Hot || st.Pool == "" {
		dot = red
	}
	level := fmt.Sprintf("L%d", st.Level)
	if st.Night {
		level += " night"
	}
	p.header(dot, level)

	// Hashrate, big; expected hashrate, efficiency and uptime under it.
	x := p.text(p.big, accent, 8, 82, fmt.Sprintf("%.2f", st.TH))
	p.text(p.small, dim, x+6, 82, "TH/s")
	var sub []string
	if st.Expected > 0 {
		sub = append(sub, fmt.Sprintf("expected %.2f", st.Expected))
	}
	if st.JPerTH > 0 {
		sub = append(sub, fmt.Sprintf("%.1f J/TH", st.JPerTH))
	}
	sub = append(sub, "up "+dur(st.UpS))
	p.text(p.tiny, dim, 10, 100, fit(p.tiny, strings.Join(sub, " · "), 220))

	// Three tiles: power, ASIC temperatures (coloured by threshold), fan.
	power := "--"
	if st.PowerW >= 0 {
		power = fmt.Sprintf("%d W", st.PowerW)
	}
	temp, tc := "--", fg
	if st.TempMean >= 0 {
		temp = fmt.Sprintf("%.0f / %.0f", st.TempMean, st.TempMax)
		switch {
		case st.Hot || st.TempMax >= 95:
			tc = red
		case st.TempMax >= 94:
			tc = yellow
		}
	}
	fan := "stock"
	if st.Fan >= 0 {
		fan = fmt.Sprintf("%d %%", st.Fan)
	}
	p.tile(8, 108, 70, "POWER", power, fg)
	p.tile(82, 108, 84, "ASIC °C", temp, tc)
	p.tile(170, 108, 62, "FAN", fan, fg)

	// Shares and the pool's view of this worker.
	y := 170
	p.text(p.tiny, dim, 10, y, "SHARES")
	sc := green
	if st.Rejected > 0 {
		sc = yellow
	}
	if st.Pool == "" {
		p.text(p.small, red, 72, y+1, "pool lost")
	} else {
		p.text(p.small, sc, 72, y+1, fmt.Sprintf("%d / %d", st.Accepted, st.Rejected))
	}
	if st.PoolView {
		v := fmt.Sprintf("%.2f TH/s", st.PoolTHs)
		xv := p.img.Rect.Dx() - 10 - width(p.small, v)
		p.text(p.small, fg, xv, y+1, v)
		p.text(p.tiny, dim, xv-width(p.tiny, "POOL")-6, y, "POOL")
	}
	y = 190
	p.text(p.tiny, dim, 10, y, "BEST")
	if st.PoolView && st.PoolBest > 0 {
		p.text(p.small, fg, 72, y+1, si(st.PoolBest))
	} else {
		p.text(p.small, fg, 72, y+1, fmt.Sprintf("%d bits", st.Best))
	}
	if st.Hot {
		p.right(p.small, red, y+1, "safety "+st.Safety)
	}

	// The 24 h hashrate as an area chart, scaled from zero like the web page.
	x0, x1, top, base := 8, 232, 200, 228
	p.rect(x0, base, x1, base+1, line)
	if n := len(st.Hist); n >= 2 {
		hi := 0.0
		for _, v := range st.Hist {
			hi = max(hi, v)
		}
		hi *= 1.1
		for i, v := range st.Hist {
			if v <= 0 || hi <= 0 {
				continue
			}
			xa := x0 + i*(x1-x0)/n
			xb := max(x0+(i+1)*(x1-x0)/n, xa+1) // each sample owns its slice of the width
			h := int(v / hi * float64(base-top))
			p.rect(xa, base-h, xb, base, fill)
			p.rect(xa, base-h, xb, base-h+1, accent)
		}
	}
	if st.IP != "" {
		p.right(p.tiny, dim, 239, st.IP)
	}
}
