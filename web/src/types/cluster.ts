/** GET /cluster (Go: pkg/server/cluster.go clusterResponse). */

import { oneOf } from './common'
import { hostRole, type HostOS, type HostRole } from './host'

export const CLUSTER_DISTRIBUTIONS = ['kubeadm', 'k0s'] as const
export type ClusterDistribution = (typeof CLUSTER_DISTRIBUTIONS)[number]

export const CONTROL_PLANE_MODES = ['managed', 'external'] as const
export type ControlPlaneMode = (typeof CONTROL_PLANE_MODES)[number]

export const CLUSTER_CNIS = ['cilium', 'calico', 'flannel', 'none'] as const
export type ClusterCNI = (typeof CLUSTER_CNIS)[number]

export interface ClusterHost {
  mac: string
  hostname: string
  os: HostOS | ''
  role: HostRole
  /** RFC3339 timestamp of the last Ignition fetch, or "" if never booted. */
  booted: string
  secureBoot: boolean
}

/**
 * GET /cluster response. `caFingerprint` is the CA's hash, never key material.
 * `source` says whose facts `ready`, `endpoint` and `caFingerprint` are:
 * `managed` (Booty's CA and the control-plane host's ready report) or
 * `external` (the API server Booty talks to, where `ready` means `connected`).
 * `connected`/`nodes`/`apiServer` describe the live API connection in both
 * modes; older servers omit them.
 */
export interface ClusterInfo {
  distribution: ClusterDistribution
  controlPlane: ControlPlaneMode
  source: ControlPlaneMode
  endpoint: string
  apiServer: string
  cni: ClusterCNI
  ready: boolean
  connected: boolean
  nodes: number
  caFingerprint: string
  hosts: ClusterHost[]
  warnings: string[]
}

export type RawClusterHost = Partial<Omit<ClusterHost, 'role'>> & { role?: HostRole | '' }

export type RawClusterInfo = Partial<Omit<ClusterInfo, 'hosts' | 'warnings'>> & {
  hosts?: RawClusterHost[]
  warnings?: (string | null)[]
}

export function normalizeClusterInfo(raw: RawClusterInfo | null | undefined): ClusterInfo {
  const controlPlane = oneOf(raw?.controlPlane, CONTROL_PLANE_MODES, 'external')
  return {
    distribution: oneOf(raw?.distribution, CLUSTER_DISTRIBUTIONS, 'kubeadm'),
    controlPlane,
    source: oneOf(raw?.source, CONTROL_PLANE_MODES, controlPlane),
    endpoint: raw?.endpoint ?? '',
    apiServer: raw?.apiServer ?? '',
    cni: oneOf(raw?.cni, CLUSTER_CNIS, 'none'),
    ready: raw?.ready ?? false,
    connected: raw?.connected ?? false,
    nodes: Math.max(0, Math.trunc(raw?.nodes ?? 0)) || 0,
    caFingerprint: raw?.caFingerprint ?? '',
    hosts: (raw?.hosts ?? []).map((h) => ({
      mac: h.mac ?? '',
      hostname: h.hostname ?? '',
      os: h.os ?? '',
      role: hostRole(h.role),
      booted: h.booted ?? '',
      secureBoot: h.secureBoot ?? false
    })),
    warnings: (raw?.warnings ?? []).filter((w): w is string => typeof w === 'string' && w !== '')
  }
}
