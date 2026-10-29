/**
 * Wire types for the Booty HTTP API, split by domain. Keep in sync with the Go
 * structs in pkg/ (see web/README.md for why they are hand-written). Import
 * from `@/types`; the modules are an implementation detail.
 */

export type { StatusResponse } from './common'
export * from './host'
export * from './power'
export * from './autopilot'
export * from './info'
export * from './storage'
export * from './config'
export * from './cluster'
