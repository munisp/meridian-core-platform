import { ReactNode } from 'react'
import { NavLink, useNavigate } from 'react-router-dom'
import { clearSession, getUser } from './api'

export function PageHeader({ title, sub, actions }: { title: string; sub?: string; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-sand-900">{title}</h1>
        {sub && <p className="mt-1 text-sm text-sand-500 max-w-3xl">{sub}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

export function Badge({ tone, children }: { tone: 'green' | 'amber' | 'red' | 'sand' | 'clay' | 'moss'; children: ReactNode }) {
  const map: Record<string, string> = {
    green: 'bg-moss-100 text-moss-800',
    amber: 'bg-amber-100 text-amber-800',
    red: 'bg-red-100 text-red-800',
    sand: 'bg-sand-200 text-sand-700',
    clay: 'bg-clay-100 text-clay-800',
    moss: 'bg-moss-100 text-moss-800',
  }
  return <span className={`badge ${map[tone]}`}>{children}</span>
}

export function HealthBadge({ status }: { status: string }) {
  const tone = status === 'ok' ? 'green' : status === 'degraded' ? 'amber' : status === 'disabled' ? 'sand' : status === 'unreachable' ? 'red' : 'sand'
  return <Badge tone={tone as never}>{status}</Badge>
}

export function DevSeedTag({ source }: { source?: string }) {
  if (!source || source === 'live' || source === 'local' || source === 'catalog' || source === 'static-catalog') return null
  return <Badge tone="amber">dev seed</Badge>
}

export function StatCard({ label, value, sub, seed }: { label: string; value: ReactNode; sub?: string; seed?: boolean }) {
  return (
    <div className="card p-5">
      <div className="flex items-center justify-between">
        <div className="text-xs font-semibold uppercase tracking-wide text-sand-500">{label}</div>
        {seed && <Badge tone="amber">dev seed</Badge>}
      </div>
      <div className="mt-2 text-3xl font-semibold text-sand-900">{value}</div>
      {sub && <div className="mt-1 text-xs text-sand-500">{sub}</div>}
    </div>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="card p-10 text-center text-sm text-sand-500">{children}</div>
}

export function Modal({ open, title, onClose, children }: { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-sand-900/40 p-4" onClick={onClose}>
      <div className="card w-full max-w-lg p-6" onClick={(e) => e.stopPropagation()}>
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold text-sand-900">{title}</h2>
          <button className="text-sand-400 hover:text-sand-700 text-xl leading-none" onClick={onClose} aria-label="Close">×</button>
        </div>
        {children}
      </div>
    </div>
  )
}

const NAV = [
  { to: '/', label: 'Dashboard', icon: '◧' },
  { to: '/applications', label: 'Applications', icon: '▦' },
  { to: '/rule-packs', label: 'Rule Packs', icon: '§' },
  { to: '/gates', label: 'Gates & Reg-watch', icon: '⚑' },
  { to: '/ledger', label: 'Ledger', icon: '≣' },
  { to: '/workflows', label: 'Workflows', icon: '↻' },
  { to: '/audit', label: 'Audit & Evidence', icon: '✓' },
  { to: '/tenants', label: 'Tenants & Identity', icon: '◉' },
  { to: '/flows', label: 'Cross-Zone Flows', icon: '⇄' },
  { to: '/settings', label: 'Settings', icon: '⚙' },
]

export function Layout({ children }: { children: ReactNode }) {
  const nav = useNavigate()
  const user = getUser()
  return (
    <div className="flex min-h-screen">
      <aside className="w-60 shrink-0 border-r border-sand-200 bg-sand-50 flex flex-col">
        <div className="px-5 py-5 border-b border-sand-200">
          <div className="text-lg font-semibold tracking-tight text-sand-900">Meridian</div>
          <div className="text-xs text-sand-500 mt-0.5">NRS Management Console</div>
        </div>
        <nav className="flex-1 px-3 py-4 space-y-0.5 overflow-y-auto">
          {NAV.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.to === '/'}
              className={({ isActive }) =>
                `flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm font-medium transition-colors ${
                  isActive ? 'bg-clay-100 text-clay-900' : 'text-sand-600 hover:bg-sand-100 hover:text-sand-900'
                }`
              }
            >
              <span className="w-4 text-center opacity-70">{n.icon}</span>
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-sand-200 px-4 py-3">
          <div className="text-sm font-medium text-sand-800 truncate">{user?.name || 'Console user'}</div>
          <div className="text-xs text-sand-500 truncate">{user?.email}</div>
          <button
            className="mt-2 text-xs text-clay-700 hover:text-clay-900 font-medium"
            onClick={() => { clearSession(); nav('/login') }}
          >
            Sign out
          </button>
        </div>
      </aside>
      <main className="flex-1 min-w-0 px-8 py-7 max-w-[1400px]">{children}</main>
    </div>
  )
}
