import { describe, expect, it, vi } from 'vitest'
import userEvent from '@testing-library/user-event'
import {
  ProtocolNav,
  ProtocolSections,
  interfaceStatus,
  protocolEntries,
  useProtocolSections,
} from './ProtocolSections'
import { renderUI, screen, waitFor, within } from '../testRender'
import { HostProvider, type BoardHost } from '../lib/host'
import type { DetectedInterface } from '../lib/analyzerApi'

// Selecting on the board is an editor's control: it renders nothing on the
// site, and inside KiCad it has to reach the host with exactly the nets it
// names. The same rule DDR's group table follows.
function fakeHost(): BoardHost {
  return {
    name: 'KiCad',
    selectNets: vi.fn().mockResolvedValue(undefined),
    applyToBoard: vi.fn().mockResolvedValue({ removed: 0, added: 0, message: '' }),
    rescan: vi.fn().mockResolvedValue(undefined),
  }
}

// A board with six protocols on it ran them together under one heading that
// was really only DDR's. Each one has to say where it ends and what it found.

const iface = (over: Partial<DetectedInterface>): DetectedInterface => ({
  id: 'Ethernet RGMII (ETH1)',
  kind: 'rgmii',
  name: 'Ethernet RGMII (ETH1)',
  nets: 12,
  routed: 12,
  pairs: 0,
  evidence: 'named RGMII-style',
  summary: 'each direction is matched to its own clock',
  actionable: false,
  unroutable: 0,
  geometry: { width_mm: 0.2, gap_mm: 0.2, needs_width: false, needs_gap: false },
  impedance: {} as DetectedInterface['impedance'],
  groups: [
    {
      name: 'transmit',
      reference: 'ETH1.GTX_CLK',
      reference_mm: 40,
      spread_mm: 2,
      limit_mm: 10,
      out_of_tolerance: 0,
      unroutable: 0,
      members: 6,
    },
    {
      name: 'receive',
      reference: 'ETH1.RX_CLK',
      reference_mm: 38,
      spread_mm: 22,
      limit_mm: 10,
      out_of_tolerance: 2,
      unroutable: 0,
      members: 6,
    },
  ],
  ...over,
})

describe('ProtocolSections', () => {
  it('gives each protocol its own section, named and folded', async () => {
    const user = userEvent.setup()
    renderUI(
      <ProtocolSections
        ddr={<div>the DDR tables</div>}
        ddrStatus={{ text: '40 out of tolerance', tone: 'orange' }}
        interfaces={[
          iface({}),
          iface({ id: 'USB', name: 'USB', kind: 'usb2', groups: [], pair_skew: [{ name: 'D', p: 'D+', n: 'D-', skew_mm: 0.1, limit_mm: 0.5, routed: true, in_tolerance: true }] }),
        ]}
      />,
    )
    // Every one starts folded, DDR too: it alone runs to several screens.
    for (const name of [/DDR memory/, /Ethernet RGMII/, /USB/]) {
      expect(screen.getByRole('button', { name })).toHaveAttribute('aria-expanded', 'false')
    }
    expect(screen.getByText('40 out of tolerance')).toBeInTheDocument()
    const usb = screen.getByRole('button', { name: /USB/ })
    await user.click(usb)
    expect(usb).toHaveAttribute('aria-expanded', 'true')
  })

  it('opens the section picked in the list, and only that one', async () => {
    const user = userEvent.setup()
    const scrolled = vi.fn()
    Element.prototype.scrollIntoView = scrolled
    const interfaces = [
      iface({}),
      iface({ id: 'USB', name: 'USB', kind: 'usb2', groups: [], unroutable: 2, nets: 2, pair_skew: [{ name: 'D', p: 'D+', n: 'D-', skew_mm: 0, limit_mm: 0.5, routed: false, in_tolerance: false }] }),
    ]
    const ddrStatus = { text: 'every matched net is within tolerance', tone: 'teal', short: 'ok' }
    function Page() {
      const s = useProtocolSections()
      const entries = protocolEntries(interfaces, ddrStatus)
      return (
        <>
          <ProtocolNav
            entries={entries}
            open={s.open}
            onJump={s.jump}
            onOpenAll={() => s.setOpen(entries.map((e) => e.id))}
            onCloseAll={() => s.setOpen([])}
          />
          <ProtocolSections
            ddr={<div>the DDR tables</div>}
            ddrStatus={ddrStatus}
            interfaces={interfaces}
            open={s.open}
            onOpenChange={s.setOpen}
            withNav
          />
        </>
      )
    }
    renderUI(<Page />)
    const nav = within(screen.getByText('Protocols').closest('.mantine-Card-root') as HTMLElement)
    // Each says where it stands in a word or two.
    expect(nav.getByRole('button', { name: /DDR memory\s*ok/ })).toBeInTheDocument()
    expect(nav.getByRole('button', { name: /Ethernet RGMII \(ETH1\)\s*2 out/ })).toBeInTheDocument()
    expect(nav.getByRole('button', { name: /USB\s*not routed/ })).toBeInTheDocument()

    const section = (name: RegExp) =>
      screen.getAllByRole('button', { name }).find((b) => b.hasAttribute('aria-expanded'))!
    await user.click(section(/DDR memory/))
    await user.click(nav.getByRole('button', { name: /Ethernet RGMII/ }))
    expect(section(/Ethernet RGMII/)).toHaveAttribute('aria-expanded', 'true')
    // The one open before is folded, so the page does not grow with each jump.
    expect(section(/DDR memory/)).toHaveAttribute('aria-expanded', 'false')
    await waitFor(() => expect(scrolled).toHaveBeenCalled())

    await user.click(nav.getByRole('button', { name: /Open all/ }))
    for (const n of [/DDR memory/, /Ethernet RGMII/, /USB/]) {
      expect(section(n)).toHaveAttribute('aria-expanded', 'true')
    }
    await user.click(nav.getByRole('button', { name: /Close all/ }))
    expect(section(/USB/)).toHaveAttribute('aria-expanded', 'false')
  })

  it('shows what each group is matched to, and never the other direction', () => {
    renderUI(<ProtocolSections interfaces={[iface({})]} open={['Ethernet RGMII (ETH1)']} />)
    // The card, not just its heading: the target sits below the heading.
    const card = (name: string) => screen.getByText(name).closest('.mantine-Card-root') as HTMLElement
    const tx = card('transmit')
    expect(within(tx).getByText('ETH1.GTX_CLK')).toBeInTheDocument()
    expect(within(tx).getByText(/tolerance ±10.000 mm/)).toBeInTheDocument()
    const rx = card('receive')
    expect(within(rx).getByText('ETH1.RX_CLK')).toBeInTheDocument()
    expect(within(rx).queryByText(/GTX_CLK/)).not.toBeInTheDocument()
  })

  it('shows the length to match to large and bold, not in the dimmed heading', () => {
    renderUI(<ProtocolSections interfaces={[iface({})]} open={['Ethernet RGMII (ETH1)']} />)
    const tx = screen.getByText('transmit').closest('.mantine-Card-root') as HTMLElement
    expect(within(tx).getByText('Match to')).toBeInTheDocument()
    const ref = within(tx).getByText('ETH1.GTX_CLK')
    expect(ref).toHaveStyle({ fontWeight: '700' })
  })

  // A count of "2 out" with no way to see which two is not actionable.
  it('names the nets that are out and offers to select them', () => {
    const rows = [
      { net: '/eth/RXD0', label: 'RXD0', role: '', routed: true, length_mm: 40, delay_ps: 0, deviation_mm: 0.4, need_mm: 0, need_ps: 0, in_tolerance: true, headroom_mm: 0, needs_reroute: false },
      { net: '/eth/RXD1', label: 'RXD1', role: '', routed: true, length_mm: 62, delay_ps: 0, deviation_mm: 22, need_mm: 0, excess_mm: 12, need_ps: 0, in_tolerance: false, headroom_mm: 0, needs_reroute: false },
      { net: '/eth/RX_CLK', label: 'RX_CLK', role: 'reference', routed: true, length_mm: 38, delay_ps: 0, deviation_mm: 0, need_mm: 0, need_ps: 0, in_tolerance: true, headroom_mm: 0, needs_reroute: false, through: ['R80'] },
    ]
    renderUI(
      <ProtocolSections
        open={['Ethernet RGMII (ETH1)']}
        interfaces={[
          iface({
            groups: [
              { name: 'receive', reference: 'ETH1.RX_CLK', reference_mm: 38, spread_mm: 24, limit_mm: 10, out_of_tolerance: 1, unroutable: 0, members: 3, rows },
            ],
          }),
        ]}
      />,
    )
    expect(screen.getByText('RXD1')).toBeInTheDocument()
    expect(screen.getByText('+22.000 mm')).toBeInTheDocument()
    expect(screen.getByText('shorten 12.000 mm')).toBeInTheDocument()
    // The net that passes through a series part says so.
    expect(screen.getByText('through R80')).toBeInTheDocument()
    // No editor here, so no select button -- the same as a DDR group.
    expect(screen.queryByRole('button', { name: /Select the ones out/ })).not.toBeInTheDocument()
  })

  it('selects exactly the nets that are out, inside an editor', async () => {
    const user = userEvent.setup()
    const host = fakeHost()
    const rows = [
      { net: '/eth/RXD0', label: 'RXD0', role: '', routed: true, length_mm: 40, delay_ps: 0, deviation_mm: 0.4, need_mm: 0, need_ps: 0, in_tolerance: true, headroom_mm: 0, needs_reroute: false },
      { net: '/eth/RXD1', label: 'RXD1', role: '', routed: true, length_mm: 62, delay_ps: 0, deviation_mm: 22, need_mm: 0, excess_mm: 12, need_ps: 0, in_tolerance: false, headroom_mm: 0, needs_reroute: false },
      { net: '/eth/RXD2', label: 'RXD2', role: '', routed: false, length_mm: 0, delay_ps: 0, deviation_mm: 0, need_mm: 0, need_ps: 0, in_tolerance: false, headroom_mm: 0, needs_reroute: false },
      { net: '/eth/RX_CLK', label: 'RX_CLK', role: 'reference', routed: true, length_mm: 38, delay_ps: 0, deviation_mm: 0, need_mm: 0, need_ps: 0, in_tolerance: true, headroom_mm: 0, needs_reroute: false },
    ]
    renderUI(
      <HostProvider host={host}>
        <ProtocolSections
          open={['Ethernet RGMII (ETH1)']}
          interfaces={[
            iface({
              groups: [
                { name: 'receive', reference: 'ETH1.RX_CLK', reference_mm: 38, spread_mm: 24, limit_mm: 10, out_of_tolerance: 1, unroutable: 1, members: 4, rows },
              ],
            }),
          ]}
        />
      </HostProvider>,
    )
    await user.click(screen.getByRole('button', { name: /Select the ones out/ }))
    // The one that is out. Not the reference, and not one with no copper to
    // select.
    await waitFor(() => expect(host.selectNets).toHaveBeenCalledWith(['/eth/RXD1']))
  })

  // A filter or a series resistor splits a signal across two nets, and the
  // length shown is the sum of both. Selecting one of them would highlight
  // half of what was measured.
  it('selects every net a split signal runs on', async () => {
    const user = userEvent.setup()
    const host = fakeHost()
    const rows = [
      { net: '/mipi/CSI.D1_P', label: 'D1_P', role: '', routed: true, length_mm: 60, delay_ps: 0, deviation_mm: 22, need_mm: 22, need_ps: 0, in_tolerance: false, headroom_mm: 0, needs_reroute: false, through: ['L11'], segments: ['/mipi/CSI.D1_P', '/mipi/CSI.D1con_P'] },
      { net: '/mipi/CSI.CK_P', label: 'CK_P', role: 'reference', routed: true, length_mm: 82, delay_ps: 0, deviation_mm: 0, need_mm: 0, need_ps: 0, in_tolerance: true, headroom_mm: 0, needs_reroute: false },
    ]
    renderUI(
      <HostProvider host={host}>
        <ProtocolSections
          open={['Ethernet RGMII (ETH1)']}
          interfaces={[
            iface({
              groups: [
                { name: 'data lanes to clock', reference: 'CSI.CK_P', reference_mm: 82, spread_mm: 22, limit_mm: 0.5, out_of_tolerance: 1, unroutable: 0, members: 2, rows },
              ],
            }),
          ]}
        />
      </HostProvider>,
    )
    await user.click(screen.getByRole('button', { name: /Select the ones out/ }))
    await waitFor(() =>
      expect(host.selectNets).toHaveBeenCalledWith(['/mipi/CSI.D1_P', '/mipi/CSI.D1con_P']),
    )

    // And the same from the net's own name.
    await user.click(screen.getByRole('button', { name: 'D1_P' }))
    await waitFor(() =>
      expect(host.selectNets).toHaveBeenLastCalledWith(['/mipi/CSI.D1_P', '/mipi/CSI.D1con_P']),
    )
  })

  // The rows are measured against the target, and where the reference is a
  // pair that is the mean of its halves. Showing one half above deviations
  // taken from the mean made every row read as out by the difference.
  it('shows the length the rows are actually measured against', () => {
    const rows = [
      { net: '/mipi/CSI.D0_P', label: 'D0_P', role: '', routed: true, length_mm: 35.631, delay_ps: 0, deviation_mm: -1.797, need_mm: 1.297, need_ps: 0, in_tolerance: false, headroom_mm: 0, needs_reroute: false },
    ]
    renderUI(
      <ProtocolSections
        open={['Ethernet RGMII (ETH1)']}
        interfaces={[
          iface({
            groups: [
              { name: 'data lanes to clock', reference: 'CSI.CK_P / CSI.CK_N', reference_mm: 36.562, target_mm: 37.428, spread_mm: 2.187, limit_mm: 0.5, out_of_tolerance: 1, unroutable: 0, members: 1, rows },
            ],
          }),
        ]}
      />,
    )
    expect(screen.getByText('37.428 mm')).toBeInTheDocument()
    expect(screen.queryByText('36.562 mm')).not.toBeInTheDocument()
    expect(screen.getByText('(the mean of the pair)')).toBeInTheDocument()
    // Shorter than the target asks for length, never for shortening.
    expect(screen.getByText('1.297 mm')).toBeInTheDocument()
  })

  it('leaves out the planner’s own interface and anything with nothing measured', () => {
    renderUI(
      <ProtocolSections
        ddr={<div>the DDR tables</div>}
        interfaces={[
          iface({ id: 'DDR memory', name: 'DDR memory', kind: 'ddr', planner: 'the DDR analyser' }),
          iface({ id: 'Bare', name: 'Bare', groups: [], pair_skew: [] }),
        ]}
      />,
    )
    // DDR appears once, as the section with the tables, not twice.
    expect(screen.getAllByText(/DDR memory/)).toHaveLength(1)
    expect(screen.queryByText('Bare')).not.toBeInTheDocument()
  })

  it('summarises a protocol in one line', () => {
    expect(interfaceStatus(iface({})).text).toBe('2 out of tolerance')
    expect(interfaceStatus(iface({ groups: [] })).text).toMatch(/within tolerance/)
    expect(interfaceStatus(iface({ groups: [], unroutable: 12 })).text).toMatch(/12 of 12/)
  })
})

// Three pairs out of tolerance each get a group of their own, and are also in
// pair_skew. The header said "3 out of tolerance, 3 pairs out" for three pairs.
describe('a protocol made of bare pairs', () => {
  const pair = (name: string) => ({
    name,
    p: `/eth/${name}_P`,
    n: `/eth/${name}_N`,
    skew_mm: 0.5,
    limit_mm: 0.127,
    routed: true,
    in_tolerance: false,
  })
  const pairGroup = (name: string) => ({
    name: `pair ${name}`,
    reference: `${name}_P`,
    reference_mm: 12.451,
    spread_mm: 0.524,
    limit_mm: 0.127,
    out_of_tolerance: 1,
    unroutable: 0,
    members: 2,
  })

  it('counts each pair that is out once', () => {
    const i = iface({
      groups: ['RJ451_D1', 'RJ451_D3', 'RJ451_D4'].map(pairGroup),
      pair_skew: ['RJ451_D1', 'RJ451_D3', 'RJ451_D4', 'RJ451_D2'].map((n) =>
        n === 'RJ451_D2' ? { ...pair(n), in_tolerance: true } : pair(n),
      ),
    })
    expect(interfaceStatus(i).text).toBe('3 out of tolerance')
  })

  // A group can arrive with no member rows. That says nothing about copper,
  // and the card used to claim nothing was routed a line under its length.
  it('does not call a measured pair unrouted when it has no rows', () => {
    renderUI(
      <ProtocolSections interfaces={[iface({ groups: [pairGroup('RJ451_D4')] })]} open={['Ethernet RGMII (ETH1)']} />,
    )
    expect(screen.queryByText(/none of them routed/)).not.toBeInTheDocument()
    expect(screen.getByText('2 nets.')).toBeInTheDocument()
  })

  it('still says so when the group really is unrouted', () => {
    renderUI(
      <ProtocolSections
        interfaces={[iface({ groups: [{ ...pairGroup('RJ451_D4'), unroutable: 2, reference_mm: 0, spread_mm: 0 }] })]}
        open={['Ethernet RGMII (ETH1)']}
      />,
    )
    expect(screen.getByText(/2 nets, none of them routed end to end yet/)).toBeInTheDocument()
  })
})
