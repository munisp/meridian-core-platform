import { FormEvent, useEffect, useState } from 'react'
import { api, fmtTime } from '../api'
import { Tenant, User } from '../types'
import { Badge, Modal, PageHeader } from '../components'

interface Relation { object: string; relation: string; subject: string; plane: string }

export default function Tenants() {
  const [tenants, setTenants] = useState<Tenant[]>([])
  const [users, setUsers] = useState<User[]>([])
  const [relations, setRelations] = useState<Relation[]>([])
  const [showTenant, setShowTenant] = useState(false)
  const [showUser, setShowUser] = useState(false)
  const [tForm, setTForm] = useState({ name: '', isolation: 'row', contact_email: '' })
  const [uForm, setUForm] = useState({ email: '', name: '', roles: 'operator', tenant_id: '', password: '' })
  const [msg, setMsg] = useState('')

  function load() {
    api.get('/v1/admin/tenants').then((r) => setTenants(r.data || [])).catch(() => {})
    api.get('/v1/admin/users').then((r) => setUsers(r.data || [])).catch(() => {})
    api.get('/v1/admin/identity/relations').then((r) => setRelations(r.data || [])).catch(() => {})
  }
  useEffect(load, [])

  async function createTenant(e: FormEvent) {
    e.preventDefault()
    try {
      await api.post('/v1/admin/tenants', tForm)
      setShowTenant(false); setTForm({ name: '', isolation: 'row', contact_email: '' }); load()
    } catch (ex: any) { setMsg(ex.response?.data?.detail || 'Create failed') }
  }

  async function toggleTenantStatus(t: Tenant) {
    await api.put(`/v1/admin/tenants/${t.id}`, { status: t.status === 'active' ? 'suspended' : 'active' })
    load()
  }

  async function createUser(e: FormEvent) {
    e.preventDefault()
    try {
      await api.post('/v1/admin/users', {
        ...uForm,
        roles: uForm.roles.split(',').map((s) => s.trim()).filter(Boolean),
      })
      setShowUser(false); setUForm({ email: '', name: '', roles: 'operator', tenant_id: '', password: '' }); load()
    } catch (ex: any) { setMsg(ex.response?.data?.detail || 'Create failed') }
  }

  return (
    <div>
      <PageHeader
        title="Tenants & Identity"
        sub="Tenant isolation levels (enclave / schema / row), console users & roles, and Permify relationship tuples."
        actions={
          <>
            <button className="btn-secondary" onClick={() => setShowUser(true)}>New user</button>
            <button className="btn-primary" onClick={() => setShowTenant(true)}>New tenant</button>
          </>
        }
      />
      {msg && <div className="mb-4 rounded-lg bg-red-50 border border-red-200 px-4 py-2.5 text-sm text-red-800">{msg}</div>}

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Tenants</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Tenant</th><th className="th">Isolation</th><th className="th">Contact</th><th className="th">Created</th><th className="th">Status</th><th className="th"></th></tr>
            </thead>
            <tbody>
              {tenants.map((t) => (
                <tr key={t.id} className="hover:bg-sand-50">
                  <td className="td">
                    <div className="font-medium text-sand-900">{t.name}</div>
                    <div className="font-mono text-xs text-sand-400">{t.id}</div>
                    {t.notes && <div className="text-xs text-sand-500 mt-1">{t.notes}</div>}
                  </td>
                  <td className="td">
                    <Badge tone={t.isolation === 'enclave' ? 'clay' : t.isolation === 'schema' ? 'moss' : 'sand'}>{t.isolation}</Badge>
                  </td>
                  <td className="td text-xs">{t.contact_email}</td>
                  <td className="td text-xs">{fmtTime(t.created_at)}</td>
                  <td className="td"><Badge tone={t.status === 'active' ? 'green' : 'amber'}>{t.status}</Badge></td>
                  <td className="td">
                    <button className="btn-secondary text-xs" onClick={() => toggleTenantStatus(t)}>
                      {t.status === 'active' ? 'Suspend' : 'Activate'}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Users & roles</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">User</th><th className="th">Roles</th><th className="th">Tenant</th><th className="th">Status</th></tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id} className="hover:bg-sand-50">
                  <td className="td">
                    <div className="font-medium text-sand-900">{u.name}</div>
                    <div className="text-xs text-sand-500">{u.email}</div>
                  </td>
                  <td className="td">
                    <div className="flex flex-wrap gap-1">
                      {u.roles.map((r) => <Badge key={r} tone={r === 'admin' ? 'clay' : r === 'board' ? 'moss' : 'sand'}>{r}</Badge>)}
                    </div>
                  </td>
                  <td className="td font-mono text-xs">{u.tenant_id}</td>
                  <td className="td"><Badge tone={u.status === 'active' ? 'green' : 'red'}>{u.status}</Badge></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section>
        <h2 className="text-sm font-semibold text-sand-900 mb-3">Permify relations (dev checker)</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th className="th">Object</th><th className="th">Relation</th><th className="th">Subject</th><th className="th">Plane</th></tr>
            </thead>
            <tbody>
              {relations.map((r, i) => (
                <tr key={i} className="hover:bg-sand-50">
                  <td className="td font-mono text-xs">{r.object}</td>
                  <td className="td"><Badge tone="clay">{r.relation}</Badge></td>
                  <td className="td font-mono text-xs">{r.subject}</td>
                  <td className="td text-xs">{r.plane}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <Modal open={showTenant} title="New tenant" onClose={() => setShowTenant(false)}>
        <form onSubmit={createTenant} className="space-y-4">
          <div><label className="label">Name</label><input className="input" required value={tForm.name} onChange={(e) => setTForm({ ...tForm, name: e.target.value })} /></div>
          <div>
            <label className="label">Isolation level</label>
            <select className="input" value={tForm.isolation} onChange={(e) => setTForm({ ...tForm, isolation: e.target.value })}>
              <option value="row">row — shared schema, tenant_id discriminator</option>
              <option value="schema">schema — dedicated schema per tenant</option>
              <option value="enclave">enclave — fully separate environment</option>
            </select>
          </div>
          <div><label className="label">Contact email</label><input className="input" type="email" value={tForm.contact_email} onChange={(e) => setTForm({ ...tForm, contact_email: e.target.value })} /></div>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowTenant(false)}>Cancel</button>
            <button className="btn-primary">Create tenant</button>
          </div>
        </form>
      </Modal>

      <Modal open={showUser} title="New user" onClose={() => setShowUser(false)}>
        <form onSubmit={createUser} className="space-y-4">
          <div><label className="label">Name</label><input className="input" required value={uForm.name} onChange={(e) => setUForm({ ...uForm, name: e.target.value })} /></div>
          <div><label className="label">Email</label><input className="input" type="email" required value={uForm.email} onChange={(e) => setUForm({ ...uForm, email: e.target.value })} /></div>
          <div><label className="label">Roles (comma-separated)</label><input className="input" value={uForm.roles} onChange={(e) => setUForm({ ...uForm, roles: e.target.value })} placeholder="operator, auditor" /></div>
          <div><label className="label">Tenant id</label><input className="input" value={uForm.tenant_id} onChange={(e) => setUForm({ ...uForm, tenant_id: e.target.value })} placeholder="t-nrs-sovereign" /></div>
          <div><label className="label">Password (dev)</label><input className="input" value={uForm.password} onChange={(e) => setUForm({ ...uForm, password: e.target.value })} placeholder="defaults to changeme123" /></div>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowUser(false)}>Cancel</button>
            <button className="btn-primary">Create user</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
