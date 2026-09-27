package proto

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/embeddedci-com/pcb-autorouter/netlen"
)

// Measuring what an interface needs.
//
// Detection says what the question is; this answers it. Two questions, in fact,
// and they are not the same one:
//
//   - Do the two halves of each differential pair match each other? This is the
//     one that applies to nearly every fast interface on a board, needs no
//     knowledge of the protocol beyond "these two are a pair", and is where a
//     length matcher earns its keep on USB, PCIe and MIPI.
//   - Do the members of each group match their reference? That is the
//     source-synchronous question -- data against the clock it travels with --
//     and it needs the protocol to say which net is the clock.
//
// Nothing here decides what to do about an answer. It reports the skew and the
// limit the interface was detected with, and the limit is a starting point the
// user is expected to argue with.

// Measurer is the length engine. netlen.Engine satisfies it.
type Measurer interface {
	Measure(net string) *netlen.Measure
}

// PairSkew is how far apart the two halves of a pair are.
type PairSkew struct {
	Base string
	P, N string

	// PMM and NMM are the two routed lengths.
	PMM, NMM float64

	// SkewMM is the difference, always positive.
	SkewMM float64

	// LimitMM is what the interface asks for.
	LimitMM float64

	// Routed is false when either half has no complete route, in which case
	// the skew means nothing.
	Routed bool

	InTolerance bool
}

// GroupSkew is how a group sits against its reference.
type GroupSkew struct {
	Name      string
	Reference string

	// ReferenceMM is the reference's own routed length, zero when it is not
	// routed.
	ReferenceMM float64

	// TargetMM is what the members are actually judged against. It is the
	// reference's length, except where the reference is half of a
	// differential pair -- a camera clock lane, a DDR strobe -- and the group
	// is matched to the pair, where it is the mean of the two halves.
	TargetMM float64

	Members []MemberSkew

	// SpreadMM is the longest member minus the shortest, over the routed ones.
	SpreadMM float64

	LimitMM    float64
	OutOfTol   int
	Unroutable int
}

// MemberSkew is one net against its group's reference.
type MemberSkew struct {
	Net         string
	LengthMM    float64
	DeviationMM float64
	Routed      bool
	InTolerance bool

	// Through are the parts the signal passes through, when the net is only
	// part of it: a series resistor or a coupling capacitor splits a signal
	// into two nets, and the length above is the sum of both.
	Through []string `json:"through,omitempty"`
}

// Assessment is everything measured about one interface.
type Assessment struct {
	Pairs  []PairSkew
	Groups []GroupSkew

	// Findings counts what is out of tolerance: pairs, then group members.
	PairsOut, MembersOut int

	// Unroutable is how many of the interface's nets have no complete route,
	// which is the first thing to know: nothing can be matched over copper
	// that does not exist.
	Unroutable int

	// Summary is a sentence for the list.
	Summary string

	// plannerOut is set by WithPlannerVerdict when the interface's own
	// analyser found something out of tolerance.
	plannerOut bool
}

// Assess measures an interface.
// joiner is an engine that can follow a signal through a series part. The
// interface is asked for rather than required, so a stand-in measurer in a test
// stays a one-method thing.
type joiner interface {
	Joined(net string) *netlen.Joined
}

func (i *Interface) Assess(m Measurer) *Assessment {
	a := &Assessment{}
	j, canJoin := m.(joiner)

	// A length is the whole signal: a series resistor or coupling capacitor
	// splits a net in two, and measuring only the near side understates each
	// line of a bus by a different amount, which is the skew being looked for.
	length := func(net string) (float64, bool) {
		if canJoin {
			x := j.Joined(net)
			return x.LengthMM, x.Found
		}
		mm := m.Measure(net)
		if mm == nil || !mm.Longest.Found {
			return 0, false
		}
		return mm.Longest.Length, true
	}
	through := func(net string) []string {
		if !canJoin {
			return nil
		}
		return j.Joined(net).Through
	}
	// One signal, one row. Where a series part splits a signal and the
	// schematic names both sides -- "CSI.D1_P" into a filter, "CSI.D1con_P"
	// out of it -- detection sees two nets and both measure the whole signal,
	// so listing both would show it twice and count it twice against the
	// tolerance. The name kept is the one on the controller's side.
	sameSignal := func(nets []string) map[string]bool {
		drop := map[string]bool{}
		if !canJoin {
			return drop
		}
		keep := map[string]string{}
		for _, net := range nets {
			x := j.Joined(net)
			if !x.Split() {
				continue
			}
			segs := append([]string{}, x.Segments...)
			sort.Strings(segs)
			key := strings.Join(segs, "\x00")
			if held, ok := keep[key]; ok {
				if net == x.Main {
					drop[held], keep[key] = true, net
				} else {
					drop[net] = true
				}
				continue
			}
			keep[key] = net
		}
		return drop
	}

	seen := map[string]bool{}
	for _, net := range i.Nets {
		if mm := m.Measure(net); mm == nil || !mm.Complete || !mm.Longest.Found {
			a.Unroutable++
		}
		seen[net] = true
	}

	dropPair := sameSignal(pairNets(i.Pairs))
	for _, p := range i.Pairs {
		if dropPair[p.P] || dropPair[p.N] {
			continue
		}
		pl, pok := length(p.P)
		nl, nok := length(p.N)
		s := PairSkew{
			Base: p.Base, P: p.P, N: p.N, PMM: pl, NMM: nl,
			LimitMM: i.IntraPair.MM, Routed: pok && nok,
		}
		if s.Routed {
			s.SkewMM = math.Abs(pl - nl)
			s.InTolerance = s.LimitMM <= 0 || s.SkewMM <= s.LimitMM
			if !s.InTolerance {
				a.PairsOut++
			}
		}
		a.Pairs = append(a.Pairs, s)
	}
	sort.Slice(a.Pairs, func(x, y int) bool { return a.Pairs[x].SkewMM > a.Pairs[y].SkewMM })

	for _, g := range i.Groups {
		gs := GroupSkew{Name: g.Name, Reference: g.Reference, LimitMM: g.Tolerance.MM}
		ref, refOK := 0.0, false
		if g.Reference != "" {
			ref, refOK = length(g.Reference)
		}
		lo, hi := math.Inf(1), math.Inf(-1)
		drop := sameSignal(g.Members)
		for _, net := range g.Members {
			if drop[net] {
				continue
			}
			l, ok := length(net)
			ms := MemberSkew{Net: net, LengthMM: l, Routed: ok, Through: through(net)}
			if ok {
				lo, hi = math.Min(lo, l), math.Max(hi, l)
			} else {
				gs.Unroutable++
			}
			gs.Members = append(gs.Members, ms)
		}
		// Without a routed reference the group is judged against its own
		// longest member, which is the best available and is said out loud
		// rather than passed off as the real thing.
		if !refOK && !math.IsInf(hi, -1) {
			ref = hi
		}
		gs.ReferenceMM = ref
		// A clock lane is a differential pair, and what the data is sampled
		// against is the pair, so the target is the mean of its halves.
		// Judging against one half alone charges every member with the pair's
		// own skew, which is a separate check with a tighter limit.
		gs.TargetMM = ref
		if refOK {
			if other := otherHalf(i.Pairs, g.Reference); other != "" {
				if l, ok := length(other); ok {
					gs.TargetMM = (ref + l) / 2
				}
			}
		}
		for k := range gs.Members {
			ms := &gs.Members[k]
			if !ms.Routed {
				continue
			}
			ms.DeviationMM = ms.LengthMM - gs.TargetMM
			ms.InTolerance = gs.LimitMM <= 0 || math.Abs(ms.DeviationMM) <= gs.LimitMM
			if !ms.InTolerance {
				gs.OutOfTol++
				a.MembersOut++
			}
		}
		if !math.IsInf(lo, 1) && !math.IsInf(hi, -1) {
			gs.SpreadMM = hi - lo
		}
		sort.SliceStable(gs.Members, func(x, y int) bool {
			return math.Abs(gs.Members[x].DeviationMM) > math.Abs(gs.Members[y].DeviationMM)
		})
		a.Groups = append(a.Groups, gs)
	}

	a.Summary = a.summarise(i)
	return a
}

// otherHalf returns the other half of the pair a net belongs to, empty when it
// is not half of one.
func otherHalf(pairs []Pair, net string) string {
	for _, p := range pairs {
		switch net {
		case p.P:
			return p.N
		case p.N:
			return p.P
		}
	}
	return ""
}

// pairNets is every net of a list of pairs, P before N.
func pairNets(pairs []Pair) []string {
	out := make([]string, 0, 2*len(pairs))
	for _, p := range pairs {
		out = append(out, p.P, p.N)
	}
	return out
}

func (a *Assessment) summarise(i *Interface) string {
	switch {
	case i.Routed == 0:
		return "none of it is routed yet"
	case a.Unroutable == i.Total:
		return "it has copper, but no net on it is joined end to end yet"
	case a.PairsOut == 0 && a.MembersOut == 0 && a.Unroutable == 0 && i.Planner != "" && len(i.Groups) == 0:
		// Only the pairs were looked at here; the lengths are the planner's.
		return "its pairs are within tolerance; lengths are checked in its own section"
	case a.PairsOut == 0 && a.MembersOut == 0 && a.Unroutable == 0:
		return "everything measurable is within tolerance"
	}
	var parts []string
	if a.PairsOut > 0 {
		parts = append(parts, fmt.Sprintf("%d pair(s) out of intra-pair tolerance", a.PairsOut))
	}
	if a.MembersOut > 0 {
		parts = append(parts, fmt.Sprintf("%d net(s) out of tolerance against their reference", a.MembersOut))
	}
	if a.Unroutable > 0 {
		parts = append(parts, fmt.Sprintf("%d net(s) not fully routed", a.Unroutable))
	}
	out := parts[0]
	for k := 1; k < len(parts); k++ {
		if k == len(parts)-1 {
			out += " and " + parts[k]
			continue
		}
		out += ", " + parts[k]
	}
	return out
}

// Actionable reports whether there is anything here a length matcher could
// improve.
func (a *Assessment) Actionable() bool { return a.PairsOut > 0 || a.MembersOut > 0 || a.plannerOut }

// WithPlannerVerdict folds in what the interface's own analyser found, for an
// interface whose lengths are judged there and not here: DDR, whose line in
// the list would otherwise see only its pairs and call the rest fine. An
// empty verdict means the analyser found nothing wrong.
func (a *Assessment) WithPlannerVerdict(i *Interface, verdict string) {
	if i.Planner == "" {
		return
	}
	switch {
	case verdict != "":
		a.plannerOut = true
		if a.PairsOut == 0 && a.MembersOut == 0 && a.Unroutable == 0 {
			a.Summary = verdict
		} else {
			a.Summary += "; " + verdict
		}
	case a.PairsOut == 0 && a.MembersOut == 0 && a.Unroutable == 0:
		a.Summary = "everything measurable is within tolerance"
	}
}
