package ddr

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/embeddedci-com/pcb-autorouter/geom"
	"github.com/embeddedci-com/pcb-autorouter/netlen"
)

// lpddr4Nets is one LPDDR4 channel, named the JEDEC way: the channel after
// every pin, _t/_c for the pair halves, _n for active low.
func lpddr4Nets(ch string) []string {
	var out []string
	for i := 0; i < 16; i++ {
		out = append(out, fmt.Sprintf("DQ%d_%s", i, ch))
	}
	for b := 0; b < 2; b++ {
		out = append(out, fmt.Sprintf("DQS%d_t_%s", b, ch), fmt.Sprintf("DQS%d_c_%s", b, ch), fmt.Sprintf("DMI%d_%s", b, ch))
	}
	for i := 0; i < 6; i++ {
		out = append(out, fmt.Sprintf("CA%d_%s", i, ch))
	}
	out = append(out, "CS0_"+ch, "CKE0_"+ch)
	// The clock last, so its detour below the bus crosses nothing.
	return append(out, "CK_t_"+ch, "CK_c_"+ch)
}

// lpddr4Board is a controller and one dual-channel LPDDR4 die, point to point,
// every net a straight 40 mm run except channel B's clock, which detours
// 10 mm further. With the command bus matched to the wrong channel's clock,
// channel B's commands would look fine and channel A's short.
func lpddr4Board(t *testing.T) *chainBoard {
	t.Helper()
	s := &chainBoard{}
	// RESET_n first, so channel B's clock is the bottom of the bus and its
	// detour crosses nothing.
	nets := append([]string{"RESET_n"}, lpddr4Nets("A")...)
	nets = append(nets, lpddr4Nets("B")...)
	s.part("U1", geom.Pt{X: 10, Y: 5}, nets)
	s.part("U2", geom.Pt{X: 50, Y: 5}, nets)
	for _, n := range nets {
		a, b := s.at["U1/"+n], s.at["U2/"+n]
		// Channel B's clock goes down 5 mm and back: 10 mm longer. The
		// complement, one row lower, turns further out, so the two nest
		// rather than cross.
		in := map[string]float64{"CK_t_B": 2, "CK_c_B": 1}[n]
		if in == 0 {
			s.track(n, a, b)
			continue
		}
		p1 := geom.Pt{X: a.X + in, Y: a.Y}
		p2 := geom.Pt{X: a.X + in, Y: a.Y + 5}
		p3 := geom.Pt{X: b.X - in, Y: b.Y + 5}
		p4 := geom.Pt{X: b.X - in, Y: b.Y}
		s.track(n, a, p1)
		s.track(n, p1, p2)
		s.track(n, p2, p3)
		s.track(n, p3, p4)
		s.track(n, p4, b)
	}
	return s
}

func TestLPDDR4ChannelsAreFoundAndKeptApart(t *testing.T) {
	b := lpddr4Board(t).build(t)

	// Flat names, no sheet path: the scope has to come from the detector, and
	// it has to be the whole interface, not one channel of it.
	prefix, nets := Scope(b, "")
	if prefix != "" || len(nets) != 2*len(lpddr4Nets("A"))+1 {
		t.Fatalf("scope = %q with %d nets, want both channels and RESET_n (%d)", prefix, len(nets), 2*len(lpddr4Nets("A"))+1)
	}
	iface, err := Classify(b, Options{Nets: nets})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(iface.Channels, ",") != "A,B" {
		t.Errorf("channels = %v, want [A B]", iface.Channels)
	}
	if iface.Width != 32 || iface.Lanes != 4 {
		t.Errorf("x%d in %d lanes, want x32 in 4", iface.Width, iface.Lanes)
	}
	if len(iface.Unclassified) != 0 || len(iface.Notes) != 0 {
		t.Errorf("unclassified %v, notes %v", iface.Unclassified, iface.Notes)
	}
	for net, want := range map[string]struct {
		role    Role
		lane    int
		channel string
		pair    string
	}{
		"DQ0_A":    {RoleData, 0, "A", ""},
		"DQ15_A":   {RoleData, 1, "A", ""},
		"DQ0_B":    {RoleData, 2, "B", ""},
		"DQ15_B":   {RoleData, 3, "B", ""},
		"DQS1_t_B": {RoleStrobe, 3, "B", "DQS1_c_B"},
		"DQS0_c_A": {RoleStrobe, 0, "A", "DQS0_t_A"},
		"DMI1_B":   {RoleDataMask, 3, "B", ""},
		"CK_t_A":   {RoleClock, -1, "A", "CK_c_A"},
		"CK_c_B":   {RoleClock, -1, "B", "CK_t_B"},
		"CA5_B":    {RoleCommand, -1, "B", ""},
		"CS0_A":    {RoleCommand, -1, "A", ""},
		"RESET_n":  {RoleControl, -1, "", ""},
	} {
		s := iface.Signal(net)
		if s == nil {
			t.Errorf("%s not classified", net)
			continue
		}
		if s.Role != want.role || s.Lane != want.lane || s.Channel != want.channel || s.Pair != want.pair {
			t.Errorf("%s = (%s, lane %d, channel %q, pair %q), want (%s, %d, %q, %q)",
				net, s.Role, s.Lane, s.Channel, s.Pair, want.role, want.lane, want.channel, want.pair)
		}
	}

	p, err := BuildPlan(iface, netlen.New(b), DefaultRules())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range p.Groups {
		names = append(names, g.Name)
	}
	want := []string{
		"channel A byte lane 0", "channel A byte lane 1", "channel B byte lane 0", "channel B byte lane 1",
		"channel A address/command U1->U2", "channel B address/command U1->U2",
	}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("groups = %q\nwant     %q", names, want)
	}

	// Each command bus is matched to its own channel's clock and has only its
	// own channel's nets in it.
	for _, ch := range []string{"A", "B"} {
		g := group(p, "channel "+ch+" address/command U1->U2")
		if g.Reference != "CK_c_"+ch+" / CK_t_"+ch {
			t.Errorf("channel %s commands matched to %q", ch, g.Reference)
		}
		for _, m := range g.Members {
			if !strings.HasSuffix(m.Net, "_"+ch) {
				t.Errorf("channel %s command group has %s", ch, m.Net)
			}
		}
	}
	// Channel B's clock is 10 mm longer, so its commands need about that.
	gA, gB := group(p, "channel A address/command U1->U2"), group(p, "channel B address/command U1->U2")
	if d := gB.ReferenceLength - gA.ReferenceLength; math.Abs(d-10) > 0.5 {
		t.Errorf("channel B clock is %.2f mm longer than A's, want 10", d)
	}
	for _, m := range gA.Members {
		if !m.Reference && m.Need > 0.01 {
			t.Errorf("channel A %s needs %.2f mm; it matches its own clock", m.Net, m.Need)
		}
	}

	// Point to point: no fly-by chain to report as unrouted.
	if p.Chain == nil || len(p.Chain.Hops) != 0 {
		t.Errorf("chain = %+v, want no hops on a point-to-point interface", p.Chain)
	}

	// Strobe against clock: each lane against its own channel's clock.
	checks := map[string]Check{}
	for _, c := range p.Checks() {
		if c.Kind == "strobe-to-clock" {
			checks[c.Name] = c
		}
	}
	if len(checks) != 4 {
		t.Fatalf("strobe-to-clock checks: %v", checks)
	}
	// The DDR line in the interface list takes this verdict; on its own it
	// sees only the pairs, which are fine, and said everything was.
	// (The default 12.07 mm strobe-to-clock band still takes channel B's
	// -10 mm; the RK3588 preset's 6.35 mm would not.)
	if got, want := p.Summary(), "8 net(s) out of tolerance in 1 group(s)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	// AN5724's fly-by layer rules do not apply to a point-to-point bus.
	if fs := p.LayerFindings(b); fs != nil {
		t.Errorf("layer findings on a point-to-point bus: %+v", fs)
	}
	a0 := checks["channel A byte lane 0 strobe vs CLK at U2"]
	b0 := checks["channel B byte lane 0 strobe vs CLK at U2"]
	if math.Abs(a0.ValueMM) > 0.01 || math.Abs(b0.ValueMM+10) > 0.5 {
		t.Errorf("strobe-to-clock A0 %.2f, B0 %.2f; want 0 and -10", a0.ValueMM, b0.ValueMM)
	}
}

// i.MX6 writes active low as _B. With one channel of data bits, a trailing _B
// is not a channel.
func TestActiveLowBIsNotAChannel(t *testing.T) {
	s := &chainBoard{}
	var nets []string
	for i := 0; i < 16; i++ {
		nets = append(nets, fmt.Sprintf("/d/DRAM_DQ%d", i))
	}
	nets = append(nets, "/d/DRAM_SDQS0_P", "/d/DRAM_A0", "/d/DRAM_CS0_B", "/d/DRAM_RAS_B", "/d/DRAM_SDCLK0_P")
	s.part("U1", geom.Pt{X: 10, Y: 5}, nets)
	s.part("U2", geom.Pt{X: 50, Y: 5}, nets)
	iface, err := Classify(s.build(t), Options{NetPrefix: "/d/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Channels) != 0 {
		t.Errorf("channels = %v, want none", iface.Channels)
	}
	for _, n := range []string{"/d/DRAM_CS0_B", "/d/DRAM_RAS_B"} {
		if s := iface.Signal(n); s == nil || s.Role != RoleCommand || s.Channel != "" {
			t.Errorf("%s = %+v, want a command on no channel", n, s)
		}
	}
}

// TI names the true half of a pair with no suffix: DDR0_CK0 and DDR0_CK0_n.
func TestUnsuffixedTrueHalfIsPaired(t *testing.T) {
	s := &chainBoard{}
	var nets []string
	for i := 0; i < 16; i++ {
		nets = append(nets, fmt.Sprintf("/d/DDR0_DQ%d", i))
	}
	nets = append(nets, "/d/DDR0_DQS0", "/d/DDR0_DQS0_n", "/d/DDR0_CK0", "/d/DDR0_CK0_n", "/d/DDR0_CA0", "/d/DDR0_CS0_n")
	s.part("U1", geom.Pt{X: 10, Y: 5}, nets)
	s.part("U2", geom.Pt{X: 50, Y: 5}, nets)
	iface, err := Classify(s.build(t), Options{NetPrefix: "/d/"})
	if err != nil {
		t.Fatal(err)
	}
	for p, n := range map[string]string{"/d/DDR0_DQS0": "/d/DDR0_DQS0_n", "/d/DDR0_CK0": "/d/DDR0_CK0_n"} {
		sp, sn := iface.Signal(p), iface.Signal(n)
		if sp == nil || sn == nil || sp.Pair != n || sn.Pair != p || sp.Polarity != Positive {
			t.Errorf("%s / %s not paired: %+v %+v", p, n, sp, sn)
		}
	}
	if s := iface.Signal("/d/DDR0_CS0_n"); s == nil || s.Role != RoleCommand || s.Index != 0 || s.Polarity != Single {
		t.Errorf("CS0_n = %+v, want command 0, single ended", s)
	}
	if len(iface.Notes) != 0 {
		t.Errorf("notes: %v", iface.Notes)
	}
}
