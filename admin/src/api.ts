import axios from 'axios'

// Base URL: dev vite proxy forwards /v1 → admin-api (see vite.config.ts).
// Override with VITE_ADMIN_API_URL for direct calls.
export const api = axios.create({
  baseURL: import.meta.env.VITE_ADMIN_API_URL || '',
  timeout: 20000,
})

const TOKEN_KEY = 'meridian.admin.token'
const USER_KEY = 'meridian.admin.user'

export function getToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}
export function getUser(): { email: string; name: string; roles: string[] } | null {
  const raw = localStorage.getItem(USER_KEY)
  return raw ? JSON.parse(raw) : null
}
export function setSession(token: string, user: unknown) {
  localStorage.setItem(TOKEN_KEY, token)
  localStorage.setItem(USER_KEY, JSON.stringify(user))
}
export function clearSession() {
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
}

// JWT interceptor
api.interceptors.request.use((cfg) => {
  const tok = getToken()
  if (tok) cfg.headers.Authorization = `Bearer ${tok}`
  return cfg
})

api.interceptors.response.use(
  (r) => r,
  (err) => {
    if (err.response?.status === 401 && !err.config?.url?.includes('/login')) {
      clearSession()
      if (location.pathname !== '/login') location.href = '/login'
    }
    return Promise.reject(err)
  },
)

export async function login(email: string, password: string) {
  const { data } = await api.post('/v1/admin/login', { email, password })
  setSession(data.token, data.user)
  return data
}

// Browser-side sha256 (WebCrypto) for WORM evidence verification.
export async function sha256Hex(content: string): Promise<string> {
  const buf = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(content))
  return Array.from(new Uint8Array(buf))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
}

export function fmtKobo(kobo: number): string {
  return '₦' + (kobo / 100).toLocaleString('en-NG', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

export function fmtTime(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return isNaN(d.getTime()) ? ts : d.toLocaleString()
}
