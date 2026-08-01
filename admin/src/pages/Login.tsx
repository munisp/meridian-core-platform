import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { login } from '../api'
import Field from '../components/Field'

export default function Login() {
  const nav = useNavigate()
  const { t } = useTranslation('pages')
  const [email, setEmail] = useState('admin@meridian.local')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await login(email, password)
      nav('/')
    } catch (ex: any) {
      setErr(ex.response?.data?.detail || ex.response?.data?.title || 'Sign-in failed — is admin-api running on :8095?')
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
            <div role="alert" className="flex items-start gap-2 rounded-md bg-danger border border-danger-strong px-3 py-2 text-sm text-danger-on">
              <svg aria-hidden="true" viewBox="0 0 24 24" className="h-4 w-4 shrink-0 mt-0.5" fill="none" stroke="currentColor" strokeWidth="2">
                <circle cx="12" cy="12" r="10" />
                <path d="M12 8v4M12 16h.01" />
              </svg>
              {err}
            </div>
          )}
          <button className="btn-primary w-full justify-center h-11" disabled={busy}>
            {busy ? t('login.signingIn') : t('login.signIn')}
          </button>
          <div className="rounded-md bg-neutral-50 border border-neutral-200 px-3 py-2 text-xs text-stone-600">
            {t('login.devNote')} <span className="font-mono">admin@meridian.local / admin123</span>.
            JWT issued by admin-api (HS256 dev secret).
          </div>
        </form>
        <div className="mt-6 text-center text-xs text-stone-600">{t('login.footer')}</div>
      </div>
    </div>
  )
}
