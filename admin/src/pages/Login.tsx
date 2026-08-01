import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { login } from '../api'
import { BUILD_DEMO, DEMO_EMAIL, DEMO_PASSWORD, isDemoMode, setDemoOverride } from '../api/demo'
import Field from '../components/Field'

export default function Login() {
  const nav = useNavigate()
  const { t } = useTranslation('pages')
  const [demo, setDemo] = useState(isDemoMode())
  const [email, setEmail] = useState(isDemoMode() ? DEMO_EMAIL : 'admin@meridian.local')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [showDemoFallback, setShowDemoFallback] = useState(false)
  const [busy, setBusy] = useState(false)

  function toggleDemo(on: boolean) {
    setDemo(on)
    setDemoOverride(on)
    setErr('')
    setShowDemoFallback(false)
    setEmail(on ? DEMO_EMAIL : 'admin@meridian.local')
    setPassword('')
  }

  async function doLogin(e: string, p: string) {
    await login(e, p)
    nav('/')
  }

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    setShowDemoFallback(false)
    try {
      await doLogin(email, password)
    } catch (ex: any) {
      setErr(ex.response?.data?.detail || ex.response?.data?.title || 'Sign-in failed — is admin-api running on :8095?')
      if (!ex.response) setShowDemoFallback(true) // network/connection failure → offer demo
    } finally {
      setBusy(false)
    }
  }

  async function continueInDemo() {
    toggleDemo(true)
    setBusy(true)
    try {
      await doLogin(DEMO_EMAIL, DEMO_PASSWORD)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-neutral-50 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="text-3xl font-semibold tracking-tight text-brand-800">Meridian</div>
          <div className="mt-1 text-sm text-stone-600">{t('login.subtitle')}</div>
        </div>
        <form onSubmit={submit} className="card p-7 space-y-4">
          <Field label={t('login.email')} required>
            {(id) => (
              <input id={id} className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus autoComplete="username" />
            )}
          </Field>
          <Field label={t('login.password')} required>
            {(id) => (
              <input id={id} className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required placeholder="••••••••" autoComplete="current-password" />
            )}
          </Field>
          {err && (
            <div role="alert" className="rounded-md bg-danger border border-danger-strong px-3 py-2 text-sm text-danger-on">
              <div className="flex items-start gap-2">
                <svg aria-hidden="true" viewBox="0 0 24 24" className="h-4 w-4 shrink-0 mt-0.5" fill="none" stroke="currentColor" strokeWidth="2">
                  <circle cx="12" cy="12" r="10" />
                  <path d="M12 8v4M12 16h.01" />
                </svg>
                {err}
              </div>
              {showDemoFallback && (
                <button
                  type="button"
                  className="mt-2 w-full justify-center rounded-md border border-danger-strong bg-white/10 px-3 py-1.5 text-sm font-medium hover:bg-white/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
                  onClick={continueInDemo}
                  disabled={busy}
                >
                  Continue in demo mode
                </button>
              )}
            </div>
          )}
          <button className="btn-primary w-full justify-center h-11" disabled={busy}>
            {busy ? t('login.signingIn') : t('login.signIn')}
          </button>
          {!BUILD_DEMO && (
            <label className="flex items-center gap-2 text-sm text-stone-700">
              <input
                type="checkbox"
                className="h-4 w-4 rounded border-neutral-300 text-brand-700 focus:ring-brand-700"
                checked={demo}
                onChange={(e) => toggleDemo(e.target.checked)}
              />
              Use demo mode <span className="text-xs text-stone-500">(seeded data, no live backend)</span>
            </label>
          )}
          {demo ? (
            <div className="rounded-md bg-warning border border-warning-strong px-3 py-2 text-xs text-warning-on">
              <span className="font-semibold">DEMO MODE</span> — seeded data, no live backend. Sign in with{' '}
              <span className="font-mono">{DEMO_EMAIL} / {DEMO_PASSWORD}</span>.
            </div>
          ) : (
            <div className="rounded-md bg-neutral-50 border border-neutral-200 px-3 py-2 text-xs text-stone-600">
              {t('login.devNote')} <span className="font-mono">admin@meridian.local / admin123</span>.
              JWT issued by admin-api (HS256 dev secret).
            </div>
          )}
        </form>
        <div className="mt-6 text-center text-xs text-stone-600">{t('login.footer')}</div>
      </div>
    </div>
  )
}
