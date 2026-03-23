import { createSignal, createResource, Show } from 'solid-js';
import type { TimeRange, Verdict } from '@/api/rightsizing';
import { fetchRightSizing, exportRightSizingCSV } from '@/api/rightsizing';
import { SummaryCards } from '@/components/RightSizing/SummaryCards';
import { GuestTable } from '@/components/RightSizing/GuestTable';
import ScaleIcon from 'lucide-solid/icons/scale';
import DownloadIcon from 'lucide-solid/icons/download';

const RANGES: { value: TimeRange; label: string }[] = [
  { value: '1h', label: '1 Hour' },
  { value: '6h', label: '6 Hours' },
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '14d', label: '14 Days' },
  { value: '30d', label: '30 Days' },
];

export default function RightSizingPage() {
  const [range, setRange] = createSignal<TimeRange>('7d');
  const [searchQuery, setSearchQuery] = createSignal('');
  const [verdictFilter, setVerdictFilter] = createSignal<Verdict | 'all'>('all');

  const [data] = createResource(range, fetchRightSizing);

  return (
    <div class="p-6 space-y-6">
      {/* Header */}
      <div class="flex items-center justify-between">
        <div class="flex items-center gap-3">
          <ScaleIcon class="w-6 h-6 text-gray-500" />
          <h1 class="text-2xl font-bold text-gray-900 dark:text-gray-100">
            Right-Sizing
          </h1>
        </div>
        <div class="flex items-center gap-3">
          {/* Range selector */}
          <select
            class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm"
            value={range()}
            onChange={(e) => setRange(e.currentTarget.value as TimeRange)}
          >
            {RANGES.map(r => (
              <option value={r.value}>{r.label}</option>
            ))}
          </select>

          {/* CSV Export */}
          <button
            class="flex items-center gap-1.5 rounded-md border border-gray-300 dark:border-gray-600 px-3 py-1.5 text-sm hover:bg-gray-50 dark:hover:bg-gray-700"
            onClick={() => exportRightSizingCSV(range())}
          >
            <DownloadIcon class="w-4 h-4" />
            Export CSV
          </button>
        </div>
      </div>

      {/* Loading state */}
      <Show when={data.loading}>
        <div class="text-center py-8 text-gray-500">Analyzing fleet...</div>
      </Show>

      {/* Error state */}
      <Show when={data.error}>
        <div class="text-center py-8 text-red-500">
          Failed to load right-sizing data: {String(data.error)}
        </div>
      </Show>

      <Show when={data()}>
        {(result) => (
          <>
            {/* Data quality warning for daily-tier ranges */}
            <Show when={result().dataQuality === 'low'}>
              <div class="rounded-md bg-amber-50 dark:bg-amber-900/20 border border-amber-200 dark:border-amber-800 p-3 text-sm text-amber-700 dark:text-amber-300">
                ⓘ {result().dataQualityNote}
              </div>
            </Show>

            {/* Summary Cards */}
            <SummaryCards summary={result().summary} />

            {/* Toolbar: search + filter + stats */}
            <div class="flex items-center gap-4">
              <input
                type="text"
                placeholder="Search by name, node, or VMID..."
                class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm flex-1 max-w-sm"
                value={searchQuery()}
                onInput={(e) => setSearchQuery(e.currentTarget.value)}
              />
              <select
                class="rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-800 px-3 py-1.5 text-sm"
                value={verdictFilter()}
                onChange={(e) => setVerdictFilter(e.currentTarget.value as Verdict | 'all')}
              >
                <option value="all">All Verdicts</option>
                <option value="idle">Idle</option>
                <option value="over-provisioned">Over-provisioned</option>
                <option value="right-sized">Right-sized</option>
                <option value="under-provisioned">Under-provisioned</option>
                <option value="mixed">Mixed</option>
                <option value="insufficient-data">Insufficient Data</option>
              </select>
              <span class="text-xs text-gray-400">
                {result().summary.totalGuests} guests · {result().computeTimeMs}ms
              </span>
            </div>

            {/* Guest Table */}
            <GuestTable
              guests={result().guests}
              searchQuery={searchQuery()}
              verdictFilter={verdictFilter()}
            />
          </>
        )}
      </Show>
    </div>
  );
}
