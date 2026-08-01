// Demo-mode state for static/preview hosting where no live admin-api exists.
//
// Demo mode is active when either:
//   - the bundle was built with VITE_DEMO_MODE=1 (static preview artifacts), or
//   - the user toggled "Use demo mode" on the login page (persisted here).
//
// When active, api.ts swaps axios to the in-memory mock adapter (api/mock.ts)
// and all data is seeded, clearly-labelled demo data.

const DEMO_KEY = 'meridian.admin.demo'

export const DEMO_EMAIL = 'admin@meridian.gov.ng'
export const DEMO_PASSWORD = 'MeridianDemo2026'

/** Build-time demo flag (baked into VITE_DEMO_MODE=1 artifacts). */
export const BUILD_DEMO = import.meta.env.VITE_DEMO_MODE === '1'

/** Runtime demo override chosen on the login page. */
export function getDemoOverride(): boolean {
  try {
    return localStorage.getItem(DEMO_KEY) === '1'
  } catch {
    return false
  }
}

export function setDemoOverride(on: boolean) {
  try {
    if (on) localStorage.setItem(DEMO_KEY, '1')
    else localStorage.removeItem(DEMO_KEY)
  } catch {
    /* storage unavailable — session-only demo mode */
  }
}

/** Whether demo mode is currently active (build flag OR runtime toggle). */
export function isDemoMode(): boolean {
  return BUILD_DEMO || getDemoOverride()
}
