import { FormEvent, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, fmtTime } from '../api'
import { Tenant, User } from '../types'
import { Badge, Modal, PageHeader } from '../components'
import Field from '../components/Field'

interface Relation { object: string; relation: string; subject: string; plane: string }

export default function Tenants() {
  const { t: tr } = useTranslation('pages')
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
    } catch (ex: any) { setMsg(ex.response?.data?.detail || tr('tenants.createFailed')) }
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
        title={tr('tenants.title')}
        sub={tr('tenants.sub')}
        actions={
          <>
            <button className="btn-secondary" onClick={() => setShowUser(true)}>{tr('tenants.newUser')}</button>
            <button className="btn-primary" onClick={() => setShowTenant(true)}>{tr('tenants.newTenant')}</button>
          </>
        }
      />
      {msg && <div role="alert" className="mb-4 rounded-lg bg-danger border border-danger-strong px-4 py-2.5 text-sm text-danger-on">{msg}</div>}

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{tr('tenants.tenantsTitle')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{tr('tenants.th.tenant')}</th><th scope="col" className="th">{tr('tenants.th.isolation')}</th><th scope="col" className="th">{tr('tenants.th.contact')}</th><th scope="col" className="th">{tr('tenants.th.created')}</th><th scope="col" className="th">{tr('tenants.th.status')}</th><th scope="col" className="th"></th></tr>
            </thead>
            <tbody>
              {tenants.map((t) => (
                <tr key={t.id} className="hover:bg-neutral-50">
                  <td className="td">
                    <div className="font-medium text-stone-900">{t.name}</div>
                    <div className="font-mono text-xs text-stone-600">{t.id}</div>
                    {t.notes && <div className="text-xs text-stone-600 mt-1">{t.notes}</div>}
                  </td>
                  <td className="td">
                    <Badge tone={t.isolation === 'enclave' ? 'clay' : t.isolation === 'schema' ? 'moss' : 'sand'}>{t.isolation}</Badge>
                  </td>
                  <td className="td text-xs">{t.contact_email}</td>
                  <td className="td text-xs">{fmtTime(t.created_at)}</td>
                  <td className="td"><Badge tone={t.status === 'active' ? 'green' : 'amber'}>{t.status}</Badge></td>
                  <td className="td">
                    <button className="btn-secondary text-xs" onClick={() => toggleTenantStatus(t)}>
                      {t.status === 'active' ? tr('tenants.suspend') : tr('tenants.activate')}
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="mb-8">
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{tr('tenants.usersTitle')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{tr('tenants.usersTh.user')}</th><th scope="col" className="th">{tr('tenants.usersTh.roles')}</th><th scope="col" className="th">{tr('tenants.usersTh.tenant')}</th><th scope="col" className="th">{tr('tenants.usersTh.status')}</th></tr>
            </thead>
            <tbody>
              {users.map((u) => (
                <tr key={u.id} className="hover:bg-neutral-50">
                  <td className="td">
                    <div className="font-medium text-stone-900">{u.name}</div>
                    <div className="text-xs text-stone-600">{u.email}</div>
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
        <h2 className="text-sm font-semibold text-stone-900 mb-3">{tr('tenants.permifyTitle')}</h2>
        <div className="card overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr><th scope="col" className="th">{tr('tenants.permifyTh.object')}</th><th scope="col" className="th">{tr('tenants.permifyTh.relation')}</th><th scope="col" className="th">{tr('tenants.permifyTh.subject')}</th><th scope="col" className="th">{tr('tenants.permifyTh.plane')}</th></tr>
            </thead>
            <tbody>
              {relations.map((r, i) => (
                <tr key={i} className="hover:bg-neutral-50">
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

      <Modal open={showTenant} title={tr('tenants.newTenant')} onClose={() => setShowTenant(false)}>
        <form onSubmit={createTenant} className="space-y-4">
          <Field label={tr('tenants.name')} required>{(id) => <input id={id} className="input" required value={tForm.name} onChange={(e) => setTForm({ ...tForm, name: e.target.value })} />}</Field>
          <Field label={tr('tenants.isolationLevel')}>
            {(id) => (
            <select id={id} className="input" value={tForm.isolation} onChange={(e) => setTForm({ ...tForm, isolation: e.target.value })}>
              <option value="row">{tr('tenants.isolationRow')}</option>
              <option value="schema">{tr('tenants.isolationSchema')}</option>
              <option value="enclave">{tr('tenants.isolationEnclave')}</option>
            </select>
            )}
          </Field>
          <Field label={tr('tenants.contactEmail')}>{(id) => <input id={id} className="input" type="email" value={tForm.contact_email} onChange={(e) => setTForm({ ...tForm, contact_email: e.target.value })} />}</Field>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowTenant(false)}>{tr('tenants.cancel')}</button>
            <button className="btn-primary">{tr('tenants.createTenant')}</button>
          </div>
        </form>
      </Modal>

      <Modal open={showUser} title={tr('tenants.newUser')} onClose={() => setShowUser(false)}>
        <form onSubmit={createUser} className="space-y-4">
          <Field label={tr('tenants.name')} required>{(id) => <input id={id} className="input" required value={uForm.name} onChange={(e) => setUForm({ ...uForm, name: e.target.value })} />}</Field>
          <Field label={tr('tenants.email')} required>{(id) => <input id={id} className="input" type="email" required value={uForm.email} onChange={(e) => setUForm({ ...uForm, email: e.target.value })} />}</Field>
          <Field label={tr('tenants.rolesLabel')}>{(id) => <input id={id} className="input" value={uForm.roles} onChange={(e) => setUForm({ ...uForm, roles: e.target.value })} placeholder={tr('tenants.rolesPlaceholder')} />}</Field>
          <Field label={tr('tenants.tenantId')}>{(id) => <input id={id} className="input" value={uForm.tenant_id} onChange={(e) => setUForm({ ...uForm, tenant_id: e.target.value })} placeholder={tr('tenants.tenantIdPlaceholder')} />}</Field>
          <Field label={tr('tenants.passwordDev')}>{(id) => <input id={id} className="input" value={uForm.password} onChange={(e) => setUForm({ ...uForm, password: e.target.value })} placeholder={tr('tenants.passwordPlaceholder')} />}</Field>
          <div className="flex justify-end gap-2">
            <button type="button" className="btn-secondary" onClick={() => setShowUser(false)}>{tr('tenants.cancel')}</button>
            <button className="btn-primary">{tr('tenants.createUser')}</button>
          </div>
        </form>
      </Modal>
    </div>
  )
}
