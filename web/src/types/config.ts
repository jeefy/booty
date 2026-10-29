/** GET /config and the /config/template editor endpoints (Go: pkg/server/config.go). */

export type SettingSource = 'flag' | 'env' | 'default'

export interface ConfigSetting {
  key: string
  /** Rendered value; "" when redacted (the server never sends secrets). */
  value: string
  source: SettingSource
  redacted: boolean
}

export type TemplateSource = 'file' | 'embedded'

export interface TemplateInfo {
  name: string
  /** `embedded` when no file exists yet and Booty renders its built-in template. */
  source: TemplateSource
  writable: boolean
}

export interface HostTemplateRef {
  mac: string
  hostname: string
  name: string
}

export interface EffectiveConfig {
  settings: ConfigSetting[]
  templates: {
    default: TemplateInfo
    hosts: HostTemplateRef[]
  }
  /** Set when the template store as a whole is read-only (e.g. mounted ConfigMap). */
  readOnlyReason?: string
}

export interface TemplateDocument extends TemplateInfo {
  content: string
  readOnlyReason?: string
}

export type ValidationKind = 'warning' | 'error'

export interface ValidationEntry {
  kind: ValidationKind
  message: string
}

/** POST /config/template/validate response (200 even when `ok` is false). */
export interface TemplateValidation {
  ok: boolean
  /** Rendered Ignition JSON; may be "" when translation failed. */
  ignition: string
  entries: ValidationEntry[]
}

export type RawEffectiveConfig = Partial<Omit<EffectiveConfig, 'settings' | 'templates'>> & {
  settings?: Partial<ConfigSetting>[]
  templates?: {
    default?: Partial<TemplateInfo>
    hosts?: Partial<HostTemplateRef>[]
  }
}

export function normalizeTemplateInfo(raw: Partial<TemplateInfo> | null | undefined): TemplateInfo {
  return {
    name: raw?.name ?? '',
    source: raw?.source ?? 'file',
    writable: raw?.writable ?? false
  }
}

export function normalizeEffectiveConfig(
  raw: RawEffectiveConfig | null | undefined
): EffectiveConfig {
  const settings: ConfigSetting[] = (raw?.settings ?? []).map((s) => ({
    key: s.key ?? '',
    value: s.value ?? '',
    source: s.source ?? 'default',
    redacted: s.redacted ?? false
  }))
  const hosts: HostTemplateRef[] = (raw?.templates?.hosts ?? [])
    .filter((h) => Boolean(h.name))
    .map((h) => ({ mac: h.mac ?? '', hostname: h.hostname ?? '', name: h.name ?? '' }))
  return {
    settings,
    templates: { default: normalizeTemplateInfo(raw?.templates?.default), hosts },
    ...(raw?.readOnlyReason ? { readOnlyReason: raw.readOnlyReason } : {})
  }
}

export function normalizeTemplateDocument(
  raw: Partial<TemplateDocument> | null | undefined
): TemplateDocument {
  return {
    ...normalizeTemplateInfo(raw),
    content: raw?.content ?? '',
    ...(raw?.readOnlyReason ? { readOnlyReason: raw.readOnlyReason } : {})
  }
}

export type RawTemplateValidation = Partial<Omit<TemplateValidation, 'entries'>> & {
  entries?: Partial<ValidationEntry>[]
}

export function normalizeTemplateValidation(
  raw: RawTemplateValidation | null | undefined
): TemplateValidation {
  return {
    ok: raw?.ok ?? false,
    ignition: raw?.ignition ?? '',
    entries: (raw?.entries ?? []).map((e) => ({
      kind: e.kind === 'warning' ? 'warning' : 'error',
      message: e.message ?? ''
    }))
  }
}
