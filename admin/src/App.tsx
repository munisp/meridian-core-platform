import { Navigate, Route, Routes } from 'react-router-dom'
import { getToken } from './api'
import { Layout } from './components'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Applications from './pages/Applications'
import RulePacks from './pages/RulePacks'
import Gates from './pages/Gates'
import Ledger from './pages/Ledger'
import Workflows from './pages/Workflows'
import Audit from './pages/Audit'
import Tenants from './pages/Tenants'
import Flows from './pages/Flows'
import Settings from './pages/Settings'
import { ReactNode } from 'react'

function Protected({ children }: { children: ReactNode }) {
  if (!getToken()) return <Navigate to="/login" replace />
  return <Layout>{children}</Layout>
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/" element={<Protected><Dashboard /></Protected>} />
      <Route path="/applications" element={<Protected><Applications /></Protected>} />
      <Route path="/rule-packs" element={<Protected><RulePacks /></Protected>} />
      <Route path="/gates" element={<Protected><Gates /></Protected>} />
      <Route path="/ledger" element={<Protected><Ledger /></Protected>} />
      <Route path="/workflows" element={<Protected><Workflows /></Protected>} />
      <Route path="/audit" element={<Protected><Audit /></Protected>} />
      <Route path="/tenants" element={<Protected><Tenants /></Protected>} />
      <Route path="/flows" element={<Protected><Flows /></Protected>} />
      <Route path="/settings" element={<Protected><Settings /></Protected>} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
