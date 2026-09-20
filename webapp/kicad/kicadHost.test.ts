import { describe, expect, it } from 'vitest'
import { sessionURL } from './kicadHost'

describe('sessionURL', () => {
  // The plugin's version reaches the page only as a query parameter, and a
  // rescan from inside the page reloads it. Dropping the parameter made the
  // footer read "Version unknown" for the rest of the session.
  it('carries the plugin version across a reload', () => {
    window.history.replaceState({}, '', '/index.html?session=old&v=0.1.4')
    expect(sessionURL('new')).toBe('/index.html?session=new&v=0.1.4')
  })

  it('leaves it out when there is none', () => {
    window.history.replaceState({}, '', '/index.html')
    expect(sessionURL('new')).toBe('/index.html?session=new')
  })
})
