// Meridian One §10 — i18next scaffold. EN default/fallback; HA/YO/IG ship
// as bundles (offline-safe, no lazy fetches). All four languages are LTR.
// Persisted per-device under `app.lang`.
//
// Two namespaces: `common` (chrome: nav, actions, status) and `pages`
// (page-body strings: headings, table headers, buttons, empty/error states).
// TODO(native-review): HA/YO/IG translations in locales/{ha,yo,ig}/*.json are
// best-effort machine drafts and must be reviewed by native speakers before
// they are treated as user-facing copy.
import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'
import en from './locales/en/common.json'
import ha from './locales/ha/common.json'
import yo from './locales/yo/common.json'
import ig from './locales/ig/common.json'
import enPages from './locales/en/pages.json'
import haPages from './locales/ha/pages.json'
import yoPages from './locales/yo/pages.json'
import igPages from './locales/ig/pages.json'

export const LANGS = ['en', 'ha', 'yo', 'ig'] as const
export type Lang = (typeof LANGS)[number]

i18n.use(initReactI18next).init({
  resources: {
    en: { common: en, pages: enPages },
    ha: { common: ha, pages: haPages },
    yo: { common: yo, pages: yoPages },
    ig: { common: ig, pages: igPages },
  },
  lng: (localStorage.getItem('app.lang') as Lang) || 'en',
  fallbackLng: 'en',
  ns: ['common', 'pages'],
  defaultNS: 'common',
  interpolation: { escapeValue: false },
})

export function setLang(l: Lang) {
  localStorage.setItem('app.lang', l)
  i18n.changeLanguage(l)
}

export default i18n
