import { createSignal, createMemo, For, Show } from 'solid-js';
import type { GuestResult, Verdict } from '@/api/rightsizing';
import { VerdictBadge } from './VerdictBadge';

type SortKey = 'name' | 'node' | 'vmid' | 'cpuP95' | 'memP95' | 'overall' | 'daysAtVerdict';
type SortDir = 'asc' | 'desc';

interface GuestTableProps {
  guests: GuestResult[];
  searchQuery: string;
  verdictFilter: Verdict | 'all';
}

const verdictOrder: Record<Verdict, number> = {
  'idle': 1,
  'over-provisioned': 2,
  'under-provisioned': 3,
  'mixed': 4,
  'right-sized': 5,
  'insufficient-data': 6,
};

export function GuestTable(props: GuestTableProps) {
  const [sortKey, setSortKey] = createSignal<SortKey>('overall');
  const [sortDir, setSortDir] = createSignal<SortDir>('asc');

  const toggleSort = (key: SortKey) => {
    if (sortKey() === key) {
      setSortDir(d => d === 'asc' ? 'desc' : 'asc');
    } else {
      setSortKey(key);
      setSortDir('asc');
    }
  };

  const filtered = createMemo(() => {
    let list = props.guests;
    if (props.verdictFilter !== 'all') {
      list = list.filter(g => g.overall === props.verdictFilter);
    }
    if (props.searchQuery) {
      const q = props.searchQuery.toLowerCase();
      list = list.filter(g =>
        g.name.toLowerCase().includes(q) ||
        g.node.toLowerCase().includes(q) ||
        String(g.vmid).includes(q)
      );
    }
    return list;
  });

  const sorted = createMemo(() => {
    const list = [...filtered()];
    const dir = sortDir() === 'asc' ? 1 : -1;
    list.sort((a, b) => {
      let cmp = 0;
      switch (sortKey()) {
        case 'name': cmp = a.name.localeCompare(b.name); break;
        case 'node': cmp = a.node.localeCompare(b.node); break;
        case 'vmid': cmp = a.vmid - b.vmid; break;
        case 'cpuP95': cmp = a.cpuP95 - b.cpuP95; break;
        case 'memP95': cmp = a.memP95 - b.memP95; break;
        case 'overall': cmp = (verdictOrder[a.overall] ?? 6) - (verdictOrder[b.overall] ?? 6); break;
        case 'daysAtVerdict': cmp = a.daysAtVerdict - b.daysAtVerdict; break;
      }
      return cmp * dir;
    });
    return list;
  });

  const SortHeader = (p: { key: SortKey; label: string; class?: string }) => (
    <th
      class={`px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400 cursor-pointer hover:text-gray-700 dark:hover:text-gray-200 ${p.class ?? ''}`}
      onClick={() => toggleSort(p.key)}
    >
      {p.label}
      <Show when={sortKey() === p.key}>
        <span class="ml-1">{sortDir() === 'asc' ? '↑' : '↓'}</span>
      </Show>
    </th>
  );

  return (
    <div class="overflow-x-auto">
      <table class="min-w-full divide-y divide-gray-200 dark:divide-gray-700">
        <thead class="bg-gray-50 dark:bg-gray-800">
          <tr>
            <SortHeader key="name" label="Name" />
            <SortHeader key="node" label="Node" />
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">Type</th>
            <SortHeader key="vmid" label="VMID" />
            <SortHeader key="cpuP95" label="CPU P95" />
            <SortHeader key="memP95" label="Mem P95" />
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">CPU</th>
            <th class="px-3 py-2 text-left text-xs font-medium text-gray-500">Mem</th>
            <SortHeader key="overall" label="Overall" />
            <SortHeader key="daysAtVerdict" label="Days" />
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-200 dark:divide-gray-700">
          <For each={sorted()}>
            {(guest) => (
              <tr class="hover:bg-gray-50 dark:hover:bg-gray-800/50">
                <td class="px-3 py-2 text-sm font-medium">{guest.name}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.node}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.type}</td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.vmid}</td>
                <td class="px-3 py-2 text-sm">{guest.cpuP95.toFixed(1)}%</td>
                <td class="px-3 py-2 text-sm">{guest.memP95.toFixed(1)}%</td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.cpuVerdict} /></td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.memVerdict} /></td>
                <td class="px-3 py-2"><VerdictBadge verdict={guest.overall} /></td>
                <td class="px-3 py-2 text-sm text-gray-500">{guest.daysAtVerdict}d</td>
              </tr>
            )}
          </For>
        </tbody>
      </table>
      <Show when={sorted().length === 0}>
        <div class="text-center py-8 text-gray-500">
          No guests match the current filters.
        </div>
      </Show>
    </div>
  );
}
