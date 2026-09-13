import { useLiveWorkloads, type StreamState } from "./useLiveWorkloads";
import type { Workload } from "./gen/marstack/govern/v1/catalog_pb";
import { Workload_Health } from "./gen/marstack/govern/v1/catalog_pb";
import type { Freshness } from "./gen/marstack/govern/v1/common_pb";

export function App() {
  const { workloads, freshness, streamState, isLoading, error, refetch } =
    useLiveWorkloads();

  return (
    <div className="min-h-full bg-canvas text-ink">
      <header className="border-b border-edge px-8 py-5">
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-2">
          <h1 className="text-xl font-medium tracking-tight">Services</h1>
          <p className="font-mono text-xs text-muted">
            discovered from the kubernetes api · nothing registered by hand
          </p>
          <div className="ml-auto flex items-center gap-4">
            <StreamBadge state={streamState} />
            <FreshnessBadge freshness={freshness} />
          </div>
        </div>
        <DegradationBanner freshness={freshness} />
      </header>

      <main className="px-8 py-6">
        {error ? (
          <ErrorPanel message={error.message} onRetry={refetch} />
        ) : isLoading ? (
          <p className="font-mono text-xs text-muted">loading the projection…</p>
        ) : workloads.length === 0 ? (
          <EmptyPanel />
        ) : (
          <WorkloadTable workloads={workloads} />
        )}
      </main>
    </div>
  );
}

function WorkloadTable({ workloads }: { workloads: Workload[] }) {
  return (
    <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
            <th className="px-5 py-3 font-semibold">Health</th>
            <th className="px-5 py-3 font-semibold">Division</th>
            <th className="px-5 py-3 font-semibold">Namespace</th>
            <th className="px-5 py-3 font-semibold">Name</th>
            <th className="px-5 py-3 font-semibold">Kind</th>
            <th className="px-5 py-3 font-semibold">Replicas</th>
            <th className="px-5 py-3 font-semibold">Requests</th>
            <th className="px-5 py-3 font-semibold">Image</th>
          </tr>
        </thead>
        <tbody>
          {workloads.map((workload) => (
            <tr
              key={workload.uid}
              className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
            >
              <td className="px-5 py-3">
                <HealthDot health={workload.health} />
              </td>
              <td className="px-5 py-3 font-mono text-xs text-accent">
                {workload.division || "unassigned"}
              </td>
              <td className="px-5 py-3 font-mono text-xs text-muted">
                {workload.namespace}
              </td>
              <td className="px-5 py-3 font-medium">{workload.name}</td>
              <td className="px-5 py-3 font-mono text-xs text-muted">
                {workload.kind}
              </td>
              <td className="px-5 py-3 font-mono text-xs">
                {workload.replicasReady}/{workload.replicasDesired}
              </td>
              <td className="px-5 py-3 font-mono text-xs text-muted">
                {formatRequests(workload)}
              </td>
              <td className="max-w-[22rem] truncate px-5 py-3 font-mono text-xs text-muted">
                {workload.imageRef || "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function HealthDot({ health }: { health: Workload_Health }) {
  const style = {
    [Workload_Health.HEALTHY]: { color: "bg-healthy", label: "healthy" },
    [Workload_Health.PROGRESSING]: { color: "bg-progressing", label: "progressing" },
    [Workload_Health.DEGRADED]: { color: "bg-degraded", label: "degraded" },
    [Workload_Health.UNSPECIFIED]: { color: "bg-muted", label: "unknown" },
  }[health];

  return (
    <span className="flex items-center gap-2 font-mono text-xs">
      <span className={`inline-block h-2 w-2 rounded-full ${style.color}`} />
      {style.label}
    </span>
  );
}

function StreamBadge({ state }: { state: StreamState }) {
  const label: Record<StreamState, string> = {
    connecting: "connecting",
    live: "live",
    reconnecting: "reconnecting",
    resync: "resyncing",
  };

  const color: Record<StreamState, string> = {
    connecting: "text-progressing",
    live: "text-healthy",
    reconnecting: "text-degraded",
    resync: "text-progressing",
  };

  return (
    <span className={`font-mono text-[11px] uppercase tracking-wider ${color[state]}`}>
      ● {label[state]}
    </span>
  );
}

function FreshnessBadge({ freshness }: { freshness?: Freshness }) {
  if (!freshness?.observedAt) {
    return <span className="font-mono text-[11px] text-muted">age unknown</span>;
  }

  const observed = Number(freshness.observedAt.seconds) * 1000;
  const age = Math.max(0, Math.round((Date.now() - observed) / 1000));

  return (
    <span className="font-mono text-[11px] text-muted">
      projected {age}s ago · rv {freshness.resourceVersion || "—"}
    </span>
  );
}

function DegradationBanner({ freshness }: { freshness?: Freshness }) {
  if (!freshness?.degraded.length) {
    return null;
  }

  return (
    <div className="mt-4 rounded-xl border border-degraded/40 bg-degraded/10 px-4 py-3 font-mono text-xs text-degraded">
      {freshness.degraded.map((source) => (
        <p key={source.source}>
          {source.source} is degraded: {source.reason || "no detail reported"}
        </p>
      ))}
    </div>
  );
}

function EmptyPanel() {
  return (
    <div className="rounded-2xl border border-edge bg-surface px-6 py-10 text-center">
      <p className="text-sm">No workloads found in this cluster.</p>
      <p className="mt-2 font-mono text-xs text-muted">
        Deploy something and it appears here on its own.
      </p>
    </div>
  );
}

function ErrorPanel({
  message,
  onRetry,
}: {
  message: string;
  onRetry: () => void;
}) {
  return (
    <div className="rounded-2xl border border-degraded/40 bg-degraded/10 px-6 py-6">
      <p className="font-mono text-xs text-degraded">{message}</p>
      <button
        type="button"
        onClick={onRetry}
        className="mt-4 rounded-lg border border-edge px-3 py-1.5 font-mono text-xs text-ink hover:border-primary"
      >
        retry
      </button>
    </div>
  );
}

function formatRequests(workload: Workload) {
  const cpu = Number(workload.requested?.cpuMillicores ?? 0n);
  const memory = Number(workload.requested?.memoryBytes ?? 0n);

  if (cpu === 0 && memory === 0) {
    return "none set";
  }

  const cpuLabel = cpu >= 1000 ? `${(cpu / 1000).toFixed(2)} cores` : `${cpu}m`;
  const memoryLabel = `${(memory / 1024 / 1024).toFixed(0)}Mi`;

  return `${cpuLabel} · ${memoryLabel}`;
}
