package netlen

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/embeddedci-com/pcb-autorouter/board"
	"github.com/embeddedci-com/pcb-autorouter/geom"
)

// Signals that pass through a series part.
//
// A series resistor, ferrite or coupling capacitor splits one signal into two
// nets, and KiCad names the far side something nobody chose: an RGMII clock is
// "ETH1.RX_CLK" up to the resistor and "Net-(U9-RXD0_RXDLY)" after it. Measured
// per net, such a signal reads as only the copper on the near side, and by a
// different amount on each line of a bus, which is exactly the skew the
// matching is supposed to find.
//
// So a length here is the sum of the segments. The part itself is not measured:
// it is a lumped element a millimetre long, and every line of a bus carries the
// same one placed the same way, so what it contributes is common to the group
// and cancels out of the skew. The copper on both sides does not cancel.
//
// What must not happen is following a decoupling capacitor into a power plane.
// The guard is deliberately dumb and strict: two pads, both on real nets, and
// the far net must be small and not named like a rail.

// seriesPart matches the reference of a part a signal can run through: a
// resistor, an inductor, ferrite bead, filter or choke, or a coupling capacitor. The
// reference is not enough on its own -- a two-pin connector has two pads, and
// so does a small chip, and walking through one of those would join two
// signals that have nothing to do with each other -- so the pads have to line
// up as well.
var seriesPart = regexp.MustCompile(`^(R|L|C|FB|FL|CMC)[0-9]`)

// maxPartPads is how big a part may be and still be walked through. A
// common-mode filter or ESD array carries two or three pairs and their grounds;
// anything much larger is a chip, whatever it is called.
const maxPartPads = 16

// powerNet matches the names boards give rails. Case is normalised first.
var powerNet = regexp.MustCompile(`^(GND|AGND|DGND|PGND|VSS[A-Z0-9_]*|VCC[A-Z0-9_]*|VDD[A-Z0-9_]*|VBAT|VBUS|VREF[A-Z0-9_]*|VTT|[+-]?[0-9]+V[0-9]*|[0-9]+V[0-9]+)$`)

// diffSuffix are the endings that make two nets the halves of one pair, in the
// order they pair up: _P with _N, + with -, _T with _C.
var diffSuffix = [][2]string{{"_P", "_N"}, {"+", "-"}, {"_T", "_C"}, {"P", "N"}}

// sameDiffPair reports whether two nets are the two halves of one differential
// pair.
//
// A resistor across a pair is a terminator, not something a signal runs
// through: the 100 ohms bridging DDR_CLK_P and DDR_CLK_N sits at the far end of
// both, and walking it reports a clock twice its real length. A series part has
// a different signal on each side; a terminator has the same signal twice.
func sameDiffPair(a, b string) bool {
	x, y := strings.ToUpper(leafOf(a)), strings.ToUpper(leafOf(b))
	if x == y {
		return false
	}
	for _, s := range diffSuffix {
		for _, pair := range [][2]string{{s[0], s[1]}, {s[1], s[0]}} {
			if strings.HasSuffix(x, pair[0]) && strings.HasSuffix(y, pair[1]) &&
				strings.TrimSuffix(x, pair[0]) == strings.TrimSuffix(y, pair[1]) {
				return true
			}
		}
	}
	return false
}

func leafOf(net string) string {
	if i := strings.LastIndexByte(net, '/'); i >= 0 {
		return net[i+1:]
	}
	return net
}

// maxSeriesPads is how many pads a net may have and still be taken for one
// signal's segment. A rail has dozens; a segment between a driver and a series
// part has two, or a few more where the schematic also taps it.
const maxSeriesPads = 6

// maxSeriesHops bounds the walk, so a board that chains parts in a way this did
// not anticipate cannot turn into a tour of the whole net list.
const maxSeriesHops = 4

func isPowerName(net string) bool {
	leaf := leafOf(net)
	if i := strings.LastIndexByte(leaf, '.'); i >= 0 {
		leaf = leaf[i+1:]
	}
	return powerNet.MatchString(strings.ToUpper(leaf))
}

// SeriesLink is one hop: the part crossed and the net on its far side.
type SeriesLink struct {
	Through string // the component's reference, e.g. "R80"
	Net     string
}

// seriesLinks are the hops out of one net, sorted for a stable answer.
//
// Every part on the board is looked at once, on the first call, rather than
// once per net: a signal is measured from both ends and by several callers, and
// rescanning thousands of footprints each time made the analysis noticeably
// slower than the walking itself.
func (e *Engine) seriesLinks(net string) []SeriesLink {
	e.linksOnce.Do(e.buildLinks)
	return e.links[net]
}

func (e *Engine) buildLinks() {
	e.links = map[string][]SeriesLink{}
	for _, f := range e.b.Footprints {
		if len(f.Pads) < 2 || len(f.Pads) > maxPartPads || !seriesPart.MatchString(strings.ToUpper(f.Ref)) {
			continue
		}
		for _, p := range throughPairs(f) {
			e.addLink(f.Ref, p[0], p[1])
			e.addLink(f.Ref, p[1], p[0])
		}
	}
	for net := range e.links {
		out := e.links[net]
		sort.Slice(out, func(i, j int) bool { return out[i].Net < out[j].Net })
	}
}

// addLink records a hop from one net to the other side of a part, unless the
// far side is a rail, a net with too many pads to be one signal's segment, or
// the other half of the same pair.
func (e *Engine) addLink(ref, from, to string) {
	if isPowerName(to) || len(e.b.PadsOfNet(to)) > maxSeriesPads || sameDiffPair(from, to) {
		return
	}
	e.links[from] = append(e.links[from], SeriesLink{Through: ref, Net: to})
}

// throughPairs are the pads a signal can run through a part between, as nets.
//
// Two pads is the simple case. A common-mode filter or an ESD array carries
// several signals through one package -- the MIPI clock and one data pair
// through a ten-pad filter, say -- and each one has to come out on its own pad,
// not on whichever other pad the package happens to have.
func throughPairs(f *board.Footprint) [][2]string {
	if len(f.Pads) == 2 {
		a, b := f.Pads[0].Net, f.Pads[1].Net
		if a == "" || b == "" || a == b {
			return nil
		}
		return [][2]string{{a, b}}
	}
	return partPairs(f)
}

// partPairs works out which pad of a multi-pad part continues which, and
// returns the pairs as nets. Those are paired by where the pads are: a
// pass-through part faces one side's pads across the package to the other's,
// so the far pad is the one straight across.
//
// The pads are read in the part's own frame, and the two sides have to face
// each other across it: for every signal pad on one side there is exactly one
// on the other, straight across and by a clear margin, or nothing is paired at
// all. Two rows of a package whose signals run along it rather than across it
// pair ambiguously and are refused, and so is a package where the facing pads
// turn out to be the two halves of one pair, which is a choke across a pair
// rather than a part in series with it.
func partPairs(f *board.Footprint) [][2]string {
	type pad struct {
		along, across float64
		net           string
	}
	var signal []*board.Pad
	for _, p := range f.Pads {
		if p.Net == "" || isPowerName(p.Net) {
			continue
		}
		signal = append(signal, p)
	}
	if len(signal) < 4 || len(signal)%2 != 0 {
		return nil
	}
	rot := -f.Rot * math.Pi / 180
	local := make([]geom.Pt, len(signal))
	for i, p := range signal {
		local[i] = geom.Pt{X: p.Centre.X - f.At.X, Y: p.Centre.Y - f.At.Y}.Rotate(rot)
	}

	var found [][2]string
	for axis := 0; axis < 2; axis++ {
		pairs := facingPairs(signal, local, axis)
		if pairs == nil {
			continue
		}
		if found != nil {
			// Both ways round work, so which side is which is a guess. A
			// wrong guess joins two signals that only share a package.
			return nil
		}
		found = pairs
	}
	return found
}

// facingPairs pairs the pads on one side of an axis with those on the other,
// nil when they do not pair cleanly.
func facingPairs(pads []*board.Pad, local []geom.Pt, axis int) [][2]string {
	coord := func(i int, a int) float64 {
		if a == 0 {
			return local[i].X
		}
		return local[i].Y
	}
	var near, far []int
	for i := range pads {
		switch v := coord(i, axis); {
		case v > 0:
			near = append(near, i)
		case v < 0:
			far = append(far, i)
		default:
			return nil // sitting on the axis: no side to be on
		}
	}
	if len(near) == 0 || len(near) != len(far) {
		return nil
	}
	other := 1 - axis
	taken := map[int]bool{}
	var out [][2]string
	for _, i := range near {
		best, bestOff, second := -1, math.Inf(1), math.Inf(1)
		for _, k := range far {
			off := math.Abs(coord(i, other) - coord(k, other))
			if off < bestOff {
				best, bestOff, second = k, off, bestOff
			} else if off < second {
				second = off
			}
		}
		// Straight across, and not nearly as close to anything else: a tie is
		// a package whose pads do not face each other this way round.
		if best < 0 || taken[best] || second <= 2*bestOff+0.05 {
			return nil
		}
		taken[best] = true
		a, b := pads[i].Net, pads[best].Net
		if a == b || sameDiffPair(a, b) {
			return nil
		}
		out = append(out, [2]string{a, b})
	}
	return out
}

// Joined is one signal measured across the parts it passes through.
type Joined struct {
	// Net is the net asked about, the one the interface knows the signal by.
	Net string

	// Segments are every net the signal runs on, starting with Net.
	Segments []string

	// Through are the parts crossed, in the order they were reached.
	Through []string

	// LengthMM is the sum of each segment's own longest route. The parts
	// themselves are not counted; see the note at the top of this file.
	LengthMM float64

	// Main is the segment to call the signal by: the one that reaches the
	// biggest part on it, which is the controller rather than the connector or
	// the filter. Where both sides of a series part are named in the
	// schematic -- "CSI.D1_P" into a filter and "CSI.D1con_P" out of it -- the
	// signal would otherwise be listed twice, once from each end.
	Main string

	// Found is whether every segment had a route to measure, and Complete
	// whether every segment's pads are joined by copper.
	Found, Complete bool
}

// Split reports whether the signal really does pass through a part. A signal
// that does not is still returned, so a caller can use Joined for everything.
func (j *Joined) Split() bool { return len(j.Segments) > 1 }

// Joined measures a net together with whatever continues it through series
// parts.
//
// The answer is kept: the same signal is asked about from both ends and by
// every caller that shows a length, and the measuring underneath it is the
// expensive part of an analysis.
func (e *Engine) Joined(net string) *Joined {
	e.joinedMu.Lock()
	defer e.joinedMu.Unlock()
	if j, ok := e.joined[net]; ok {
		return j
	}
	j := e.join(net)
	if e.joined == nil {
		e.joined = map[string]*Joined{}
	}
	e.joined[net] = j
	return j
}

func (e *Engine) join(net string) *Joined {
	j := &Joined{Net: net, Found: true, Complete: true}
	seen := map[string]bool{net: true}
	queue := []string{net}

	for hop := 0; len(queue) > 0 && hop <= maxSeriesHops; hop++ {
		var next []string
		for _, n := range queue {
			j.Segments = append(j.Segments, n)
			m := e.Measure(n)
			if m == nil || !m.Longest.Found {
				j.Found = false
			} else {
				j.LengthMM += m.Longest.Length
			}
			if m == nil || !m.Complete {
				j.Complete = false
			}
			for _, l := range e.seriesLinks(n) {
				if seen[l.Net] {
					continue
				}
				seen[l.Net] = true
				j.Through = append(j.Through, l.Through)
				next = append(next, l.Net)
			}
		}
		queue = next
	}
	j.Main = e.mainSegment(j.Segments)
	return j
}

// mainSegment picks the segment that reaches the largest part, which on a
// signal running from a processor through a filter to a connector is the
// processor's side. Ties go to the first name, so the answer never depends on
// the order the board file happens to list things in.
func (e *Engine) mainSegment(segments []string) string {
	main, most := "", -1
	for _, seg := range segments {
		pads := 0
		for _, p := range e.b.PadsOfNet(seg) {
			if f := e.b.Footprint(p.Ref); f != nil && len(f.Pads) > pads {
				pads = len(f.Pads)
			}
		}
		if pads > most || (pads == most && seg < main) {
			main, most = seg, pads
		}
	}
	return main
}
