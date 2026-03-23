import type { Verdict } from '@/api/rightsizing';

const verdictConfig: Record<Verdict, { label: string; classes: string }> = {
  'idle': {
    label: 'Idle',
    classes: 'bg-gray-100 text-gray-700 dark:bg-gray-700 dark:text-gray-300',
  },
  'over-provisioned': {
    label: 'Over',
    classes: 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300',
  },
  'right-sized': {
    label: 'Right',
    classes: 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300',
  },
  'under-provisioned': {
    label: 'Under',
    classes: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
  },
  'mixed': {
    label: 'Mixed',
    classes: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
  },
  'insufficient-data': {
    label: 'No Data',
    classes: 'bg-gray-50 text-gray-400 dark:bg-gray-800 dark:text-gray-500',
  },
};

interface VerdictBadgeProps {
  verdict: Verdict;
}

export function VerdictBadge(props: VerdictBadgeProps) {
  const config = () => verdictConfig[props.verdict] ?? verdictConfig['insufficient-data'];
  return (
    <span class={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${config().classes}`}>
      {config().label}
    </span>
  );
}
