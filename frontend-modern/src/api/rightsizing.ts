/**
 * Right-Sizing API client
 *
 * Provides typed fetch functions for the GET /api/rightsizing and
 * GET /api/rightsizing/export endpoints.
 */

import { apiFetchJSON } from '@/utils/apiClient';

export type Verdict =
  | 'idle'
  | 'over-provisioned'
  | 'right-sized'
  | 'under-provisioned'
  | 'mixed'
  | 'insufficient-data';

export type TimeRange = '1h' | '6h' | '24h' | '7d' | '14d' | '30d';
export type DataQuality = 'high' | 'good' | 'low';

export interface GuestResult {
  id: string;
  name: string;
  node: string;
  type: 'vm' | 'container';
  vmid: number;
  status: string;
  cpus: number;
  maxMemBytes: number;
  cpuAvg: number;
  cpuP95: number;
  cpuMax: number;
  memAvg: number;
  memP95: number;
  memMax: number;
  cpuVerdict: Verdict;
  memVerdict: Verdict;
  overall: Verdict;
  daysAtVerdict: number;
  sampleCount: number;
}

export interface RightSizingSummary {
  totalGuests: number;
  idle: number;
  overProvisioned: number;
  rightSized: number;
  underProvisioned: number;
  mixed: number;
  insufficientData: number;
  reclaimableMemoryGB: number;
  reclaimableCPUs: number;
}

export interface RightSizingResult {
  summary: RightSizingSummary;
  guests: GuestResult[];
  range: string;
  tier: string;
  dataQuality: DataQuality;
  dataQualityNote?: string;
  computeTimeMs: number;
}

export async function fetchRightSizing(
  timeRange: TimeRange = '7d',
): Promise<RightSizingResult> {
  const params = new URLSearchParams({ range: timeRange });
  return apiFetchJSON(`/api/rightsizing?${params.toString()}`);
}

export function exportRightSizingCSV(timeRange: TimeRange = '7d'): void {
  const params = new URLSearchParams({ range: timeRange });
  window.open(`/api/rightsizing/export?${params.toString()}`, '_blank');
}
