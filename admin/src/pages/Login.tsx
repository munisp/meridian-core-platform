import { FormEvent, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { login } from '../api'

export default function Login() {
  const nav = useNavigate()
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
    <div className="flex min-h-screen items-center justify-center bg-sand-100 px-4">
      <div className="w-full max-w-sm">
        <div className="mb-8 text-center">
          <div className="text-3xl font-semibold tracking-tight text-sand-900">Meridian</div>
          <div className="mt-1 text-sm text-sand-500">NRS Unified Platform — Management Console</div>
        </div>
        <form onSubmit={submit} className="card p-7 space-y-4">
          <div>
            <label className="label">Email</label>
            <input className="input" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required autoFocus />
          </div>
          <div>
            <label className="label">Password</label>
            <input className="input" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required placeholder="••••••••" />
          </div>
          {err && <div className="rounded-lg bg-red-50 border border-red-200 px-3 py-2 text-sm text-red-800">{err}</div>}
          <button className="btn-primary w-full justify-center" disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
          <div className="rounded-lg bg-sand-50 border border-sand-200 px-3 py-2 text-xs text-sand-500">
            Dev mode — seeded admin <span className="font-mono">admin@meridian.local / admin123</span>.
            JWT issued by admin-api (HS256 dev secret).
          </div>
        </form>
        <div className="mt-6 text-center text-xs text-sand-400">Sovereign tax infrastructure · two zones · audited cross-zone flows only</div>
      </div>
    </div>
  )
}
