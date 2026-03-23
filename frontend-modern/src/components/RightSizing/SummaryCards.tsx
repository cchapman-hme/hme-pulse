import type { RightSizingSummary } from '@/api/rightsizing';

interface SummaryCardsProps {
  summary: RightSizingSummary;
}

function StatCard(props: {
  label: string;
  count: number;
  total: number;
  color: string;
}) {
  const pct = () => props.total > 0 ? Math.round((props.count / props.total) * 100) : 0;
  return (
    <div class={`rounded-lg border p-4 ${props.color}`}>
      <div class="text-2xl font-bold">{props.count}</div>
      <div class="text-sm opacity-75">{props.label}</div>
      <div class="text-xs opacity-50 mt-1">{pct()}% of fleet</div>
    </div>
  );
}

export function SummaryCards(props: SummaryCardsProps) {
  return (
    <div class="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-4">
      <StatCard label="Idle" count={props.summary.idle}
        total={props.summary.totalGuests}
        color="border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800/50" />
      <StatCard label="Over-provisioned" count={props.summary.overProvisioned}
        total={props.summary.totalGuests}
        color="border-blue-200 dark:border-blue-800 bg-blue-50 dark:bg-blue-900/20" />
      <StatCard label="Right-sized" count={props.summary.rightSized}
        total={props.summary.totalGuests}
        color="border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-900/20" />
      <StatCard label="Under-provisioned" count={props.summary.underProvisioned}
        total={props.summary.totalGuests}
        color="border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/20" />
      <StatCard label="Mixed" count={props.summary.mixed}
        total={props.summary.totalGuests}
        color="border-purple-200 dark:border-purple-800 bg-purple-50 dark:bg-purple-900/20" />
      <div class="rounded-lg border border-indigo-200 dark:border-indigo-800 bg-indigo-50 dark:bg-indigo-900/20 p-4">
        <div class="text-lg font-bold">
          {props.summary.reclaimableMemoryGB.toFixed(1)} GB
        </div>
        <div class="text-sm opacity-75">Reclaimable Memory</div>
        <div class="text-xs opacity-50 mt-1">
          {props.summary.reclaimableCPUs} vCPU cores
        </div>
      </div>
    </div>
  );
}
