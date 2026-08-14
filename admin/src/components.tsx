import { ReactNode, useEffect, useState } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  LayoutDashboard, AppWindow, BookMarked, Flag, Wallet, RefreshCcw,
  ShieldCheck, Users, ArrowLeftRight, Settings as SettingsIcon, LogOut, Menu, X, LucideIcon,
} from 'lucide-react'
import { clearSession, getUser } from './api'
import { isDemoMode } from './api/demo'
import LangSwitcher from './components/LangSwitcher'

export function PageHeader({ title, sub, actions }: { title: string; sub?: string; actions?: ReactNode }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 className="text-xl font-bold tracking-tight text-stone-900">{title}</h1>
        {sub && <p className="mt-1 text-sm text-stone-600 max-w-3xl">{sub}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

type Tone = 'green' | 'amber' | 'red' | 'sand' | 'clay' | 'moss'

// Meridian One §5 — status is always a chip: semantic surface + on-surface
// text + icon, never coloured text alone. Tone names kept for compatibility.
const TONE_STYLES: Record<Tone, { cls: string; icon: string }> = {
  green: { cls: 'bg-success text-success-on', icon: '✓' },
  moss: { cls: 'bg-success text-success-on', icon: '✓' },
  amber: { cls: 'bg-warning text-warning-on', icon: '◷' },
  red: { cls: 'bg-danger text-danger-on', icon: '✕' },
  sand: { cls: 'bg-neutral-100 text-neutral-800', icon: '◌' },
  clay: { cls: 'bg-brand-100 text-brand-800', icon: '●' },
}

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  const s = TONE_STYLES[tone]
  return (
    <span className={`badge ${s.cls}`}>
      <span aria-hidden="true">{s.icon}</span>
      {children}
    </span>
  )
}

export function HealthBadge({ status }: { status: string }) {
  const tone = status === 'ok' ? 'green' : status === 'degraded' ? 'amber' : status === 'disabled' ? 'sand' : status === 'unreachable' ? 'red' : 'sand'
  return <Badge tone={tone}>{status}</Badge>
}

/** F-13: visible API failure state with retry — replaces silent empty states. */
export function ErrorBanner({ message, onRetry }: { message: string; onRetry?: () => void }) {
  const { t } = useTranslation()
  return (
    <div role="alert" className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-danger-strong bg-danger px-4 py-3 text-sm text-danger-strong">
      <span className="min-w-0 break-words">{t('common.loadFailed', { defaultValue: 'Failed to load data' })}: {message}</span>
      {onRetry && <button type="button" className="btn-secondary text-xs shrink-0" onClick={onRetry}>{t('common.retry', { defaultValue: 'Retry' })}</button>}
    </div>
  )
}

export function DevSeedTag({ source }: { source?: string }) {
  if (!source || source === 'live' || source === 'local' || source === 'catalog' || source === 'static-catalog') return null
  return <Badge tone="amber">dev seed</Badge>
}

export function StatCard({ label, value, sub, seed }: { label: string; value: ReactNode; sub?: string; seed?: boolean }) {
  return (
    <div className="card p-5">
      <div className="flex items-center justify-between">
        <div className="text-xs font-semibold uppercase tracking-wide text-stone-600">{label}</div>
        {seed && <Badge tone="amber">dev seed</Badge>}
      </div>
      <div className="mt-2 text-3xl font-semibold tabular-nums text-stone-900">{value}</div>
      {sub && <div className="mt-1 text-xs text-stone-600">{sub}</div>}
    </div>
  )
}

/** Skeleton block matching final layout while data loads (spec §5). */
export function SkeletonRows({ rows = 4, height = 'h-12' }: { rows?: number; height?: string }) {
  return (
    <div className="space-y-2" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className={`skeleton ${height} w-full`} />
      ))}
    </div>
  )
}

export function Modal({ open, title, onClose, children }: { open: boolean; title: string; onClose: () => void; children: ReactNode }) {
  const { t } = useTranslation('common')
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])
  if (!open) return null
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-stone-900/40 p-4" onClick={onClose}>
      <div
        className="card w-full max-w-lg p-6 shadow-xl"
        role="dialog"
        aria-modal="true"
        aria-label={title}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="mb-4 flex items-center justify-between">
          <h2 className="text-lg font-semibold text-stone-900">{title}</h2>
          <button
            className="rounded-md p-1 text-stone-600 hover:text-stone-900 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-700"
            onClick={onClose}
            aria-label={t('actions.close')}
          >
            <span aria-hidden="true" className="text-xl leading-none">×</span>
          </button>
        </div>
        {children}
      </div>
    </div>
  )
}

const NAV: { to: string; key: string; icon: LucideIcon }[] = [
  { to: '/', key: 'dashboard', icon: LayoutDashboard },
  { to: '/applications', key: 'applications', icon: AppWindow },
  { to: '/rule-packs', key: 'rulePacks', icon: BookMarked },
  { to: '/gates', key: 'gates', icon: Flag },
  { to: '/ledger', key: 'ledger', icon: Wallet },
  { to: '/workflows', key: 'workflows', icon: RefreshCcw },
  { to: '/audit', key: 'audit', icon: ShieldCheck },
  { to: '/tenants', key: 'tenants', icon: Users },
  { to: '/flows', key: 'flows', icon: ArrowLeftRight },
  { to: '/settings', key: 'settings', icon: SettingsIcon },
]

/** Header connectivity pill (aria-live, offline-first signature, spec §5). */
function StatusPill() {
  const { t } = useTranslation('common')
  const [online, setOnline] = useState(navigator.onLine)
  useEffect(() => {
    const on = () => setOnline(true)
    const off = () => setOnline(false)
    window.addEventListener('online', on)
    window.addEventListener('offline', off)
    return () => { window.removeEventListener('online', on); window.removeEventListener('offline', off) }
  }, [])
  return (
    <span
      aria-live="polite"
      className={`badge ${online ? 'bg-success text-success-on' : 'bg-warning text-warning-on'}`}
    >
      <span aria-hidden="true" className={`h-1.5 w-1.5 rounded-full ${online ? 'bg-success-strong' : 'bg-warning-strong'}`} />
      {online ? 'Online' : t('status.offline')}
    </span>
  )
}

export function Layout({ children }: { children: ReactNode }) {
  const nav = useNavigate()
  const user = getUser()
  const { t } = useTranslation('common')
  return (
    <div className="flex min-h-screen">
      <a
        href="#content"
        className="sr-only focus:not-sr-only focus:absolute focus:z-50 focus:m-2 focus:rounded-md focus:bg-white focus:px-3 focus:py-2 focus:text-sm focus:text-brand-700 focus:ring-2 focus:ring-brand-700"
      >
        {t('a11y.skipToContent')}
      </a>
      <aside className="hidden md:flex w-60 shrink-0 bg-brand-800 text-white flex-col">
        <div className="px-5 py-5 border-b border-brand-700">
          <div className="text-lg font-semibold tracking-tight">{t('app.title')}</div>
          <div className="text-xs text-brand-100 mt-0.5">{t('app.subtitle')}</div>
        </div>
        <nav aria-label={t('a11y.primaryNav')} className="flex-1 px-3 py-4 space-y-0.5 overflow-y-auto">
          {NAV.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              end={n.to === '/'}
              className={({ isActive }) =>
                `flex items-center gap-2.5 rounded-md px-3 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-300 ${
                  isActive ? 'bg-brand-900 text-white font-semibold' : 'text-brand-100 hover:bg-brand-700 hover:text-white'
                }`
              }
            >
              <n.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
              {t(`nav.${n.key}`)}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-brand-700 px-4 py-3 space-y-2">
          <div className="flex flex-wrap items-center gap-1.5">
            <StatusPill />
            {isDemoMode() && (
              <span className="badge bg-warning text-warning-on" title="Demo mode: seeded in-memory data, no live backend">
                <span aria-hidden="true">◌</span>
                DEMO DATA — no live backend
              </span>
            )}
          </div>
          <div className="text-sm font-medium truncate">{user?.name || t('user.fallback')}</div>
          <div className="text-xs text-brand-100 truncate">{user?.email}</div>
          <div className="flex items-center justify-between gap-2 pt-1">
            <LangSwitcher />
            <button
              className="inline-flex items-center gap-1 rounded-md px-1.5 py-1 text-xs text-brand-100 hover:text-white font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-300"
              onClick={() => { clearSession(); nav('/login') }}
            >
              <LogOut aria-hidden="true" className="h-3.5 w-3.5" />
              {t('actions.signOut')}
            </button>
          </div>
        </div>
      </aside>
      <main id="content" className="flex-1 min-w-0 px-4 pb-24 pt-5 md:px-8 md:pb-7 md:py-7 max-w-[1400px]">{children}</main>
      <MobileBottomNav />
    </div>
  )
}

const MOBILE_TABS: { to: string; key: string; icon: LucideIcon }[] = [
  { to: '/', key: 'dashboard', icon: LayoutDashboard },
  { to: '/tenants', key: 'tenants', icon: Users },
  { to: '/rule-packs', key: 'rulePacks', icon: BookMarked },
  { to: '/audit', key: 'audit', icon: ShieldCheck },
]
const MORE_PATHS = NAV.filter((n) => !MOBILE_TABS.some((m) => m.to === n.to))

/** Mobile bottom-nav fallback (<768px): 4 tabs + More → drawer (spec §10). */
function MobileBottomNav() {
  const { t } = useTranslation(['common', 'pages'])
  const [open, setOpen] = useState(false)
  const { pathname } = useLocation()
  useEffect(() => { setOpen(false) }, [pathname])
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open])
  const moreActive = MORE_PATHS.some((n) => (n.to === '/' ? pathname === '/' : pathname.startsWith(n.to)))
  const tabCls = (isActive: boolean) =>
    `flex min-h-[44px] flex-col items-center justify-center gap-0.5 py-1.5 text-[10px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-700 ${
      isActive ? 'text-brand-700' : 'text-stone-600 hover:text-stone-900'
    }`
  return (
    <>
      {open && (
        <div className="fixed inset-0 z-40 bg-stone-900/40 md:hidden" onClick={() => setOpen(false)}>
          <div
            role="dialog"
            aria-modal="true"
            aria-label={t('pages:bottomNav.moreTitle')}
            className="absolute inset-x-0 bottom-0 rounded-t-2xl bg-white p-4 pb-6 shadow-xl"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="mb-2 flex items-center justify-between">
              <h2 className="text-sm font-semibold text-stone-900">{t('pages:bottomNav.moreTitle')}</h2>
              <button
                className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-stone-600 hover:text-stone-900 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-700"
                onClick={() => setOpen(false)}
                aria-label={t('actions.close')}
              >
                <X aria-hidden="true" className="h-5 w-5" />
              </button>
            </div>
            <nav aria-label={t('pages:bottomNav.moreTitle')} className="grid grid-cols-2 gap-1">
              {MORE_PATHS.map((n) => (
                <NavLink
                  key={n.to}
                  to={n.to}
                  end={n.to === '/'}
                  className={({ isActive }) =>
                    `flex min-h-[44px] items-center gap-2.5 rounded-md px-3 py-2.5 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-brand-700 ${
                      isActive ? 'bg-brand-100 text-brand-800 font-semibold' : 'text-stone-700 hover:bg-neutral-100'
                    }`
                  }
                >
                  <n.icon aria-hidden="true" className="h-4 w-4 shrink-0" />
                  {t(`nav.${n.key}`)}
                </NavLink>
              ))}
            </nav>
          </div>
        </div>
      )}
      <nav
        aria-label={t('pages:bottomNav.label')}
        className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-5 border-t border-neutral-200 bg-white pb-[env(safe-area-inset-bottom)] md:hidden"
      >
        {MOBILE_TABS.map((n) => (
          <NavLink key={n.to} to={n.to} end={n.to === '/'} className={({ isActive }) => tabCls(isActive)}>
            <n.icon aria-hidden="true" className="h-5 w-5" />
            <span className="max-w-full truncate px-0.5">{t(`nav.${n.key}`)}</span>
          </NavLink>
        ))}
        <button
          type="button"
          className={tabCls(moreActive)}
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          aria-current={moreActive ? 'page' : undefined}
        >
          <Menu aria-hidden="true" className="h-5 w-5" />
          <span className="max-w-full truncate px-0.5">{t('pages:bottomNav.more')}</span>
        </button>
      </nav>
    </>
  )
}
