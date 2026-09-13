import { useState } from "react";

import { useStreamState, type StreamState } from "./events";
import { useLiveWorkloads } from "./useLiveWorkloads";
import { useLiveDivisions } from "./useLiveDivisions";
import type { Workload } from "./gen/marstack/govern/v1/catalog_pb";
import { Workload_Health } from "./gen/marstack/govern/v1/catalog_pb";
import type { Freshness } from "./gen/marstack/govern/v1/common_pb";
import type { Division, Namespace } from "./gen/marstack/govern/v1/tenancy_pb";
import { Division_Phase } from "./gen/marstack/govern/v1/tenancy_pb";

type View = "services" | "divisions";

const views: { id: View; label: string; subtitle: string }[] = [
  {
    id: "services",
    label: "Services",
    subtitle: "discovered from the kubernetes api · nothing registered by hand",
  },
  {
    id: "divisions",
    label: "Divisions",
    subtitle: "quota, isolation and access, reconciled from the Division resource",
  },
];

export function App() {
  const [view, setView] = useState<View>("services");
  const streamState = useStreamState();
  const active = views.find((candidate) => candidate.id === view) ?? views[0];

  return (
    <div className="min-h-full bg-canvas text-ink">
      <header className="border-b border-edge px-8 py-5">
        <div className="flex flex-wrap items-center gap-x-6 gap-y-3">
          <nav className="flex items-center gap-1">
            {views.map((candidate) => (
              <button
                key={candidate.id}
                type="button"
                onClick={() => setView(candidate.id)}
                className={`rounded-lg px-3 py-1.5 text-sm transition-colors ${
                  candidate.id === view
                    ? "bg-white/10 font-medium text-ink"
                    : "text-muted hover:text-ink"
                }`}
              >
                {candidate.label}
              </button>
            ))}
          </nav>
          <p className="font-mono text-xs text-muted">{active.subtitle}</p>
          <div className="ml-auto">
            <StreamBadge state={streamState} />
          </div>
        </div>
      </header>

      <main className="px-8 py-6">
        {view === "services" ? <ServicesView /> : <DivisionsView />}
      </main>
    </div>
  );
}

function ServicesView() {
  const { workloads, freshness, isLoading, error, refetch } = useLiveWorkloads();

  return (
    <Panel
      freshness={freshness}
      isLoading={isLoading}
      error={error}
      onRetry={refetch}
      isEmpty={workloads.length === 0}
      emptyTitle="No workloads found in this cluster."
      emptyHint="Deploy something and it appears here on its own."
    >
      <WorkloadTable workloads={workloads} />
    </Panel>
  );
}

function DivisionsView() {
  const { divisions, namespaces, freshness, isLoading, error, refetch } =
    useLiveDivisions();

  return (
    <Panel
      freshness={freshness}
      isLoading={isLoading}
      error={error}
      onRetry={refetch}
      isEmpty={divisions.length === 0}
      emptyTitle="No divisions have been declared."
      emptyHint="Apply a Division resource and its namespaces are provisioned for you."
    >
      <div className="flex flex-col gap-8">
        <DivisionTable divisions={divisions} />
        <NamespaceTable namespaces={namespaces} />
      </div>
    </Panel>
  );
}

function Panel({
  freshness,
  isLoading,
  error,
  onRetry,
  isEmpty,
  emptyTitle,
  emptyHint,
  children,
}: {
  freshness?: Freshness;
  isLoading: boolean;
  error: Error | null;
  onRetry: () => void;
  isEmpty: boolean;
  emptyTitle: string;
  emptyHint: string;
  children: React.ReactNode;
}) {
  if (error) {
    return <ErrorPanel message={error.message} onRetry={onRetry} />;
  }

  if (isLoading) {
    return <p className="font-mono text-xs text-muted">loading the projection…</p>;
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-end">
        <FreshnessBadge freshness={freshness} />
      </div>
      <DegradationBanner freshness={freshness} />
      {isEmpty ? <EmptyPanel title={emptyTitle} hint={emptyHint} /> : children}
    </div>
  );
}

function WorkloadTable({ workloads }: { workloads: Workload[] }) {
  return (
    <Table
      headers={[
        "Health",
        "Division",
        "Namespace",
        "Name",
        "Kind",
        "Replicas",
        "Requests",
        "Image",
      ]}
    >
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
          <td className="px-5 py-3 font-mono text-xs text-muted">{workload.kind}</td>
          <td className="px-5 py-3 font-mono text-xs">
            {workload.replicasReady}/{workload.replicasDesired}
          </td>
          <td className="px-5 py-3 font-mono text-xs text-muted">
            {formatCompute(
              workload.requested?.cpuMillicores,
              workload.requested?.memoryBytes,
            )}
          </td>
          <td className="max-w-[22rem] truncate px-5 py-3 font-mono text-xs text-muted">
            {workload.imageRef || "—"}
          </td>
        </tr>
      ))}
    </Table>
  );
}

function DivisionTable({ divisions }: { divisions: Division[] }) {
  return (
    <section className="flex flex-col gap-3">
      <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">
        Divisions
      </h2>
      <Table headers={["Phase", "Name", "Display", "Quota", "Used", "Saturation", "Namespaces"]}>
        {divisions.map((division) => (
          <tr
            key={division.uid}
            className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
          >
            <td className="px-5 py-3">
              <PhaseBadge phase={division.phase} />
            </td>
            <td className="px-5 py-3 font-mono text-xs text-accent">{division.name}</td>
            <td className="px-5 py-3 font-medium">{division.displayName}</td>
            <td className="px-5 py-3 font-mono text-xs text-muted">
              {formatCompute(
                division.quota?.cpuMillicores,
                division.quota?.memoryBytes,
              )}
            </td>
            <td className="px-5 py-3 font-mono text-xs text-muted">
              {formatCompute(division.used?.cpuMillicores, division.used?.memoryBytes)}
            </td>
            <td className="px-5 py-3">
              <Saturation
                used={Number(division.used?.cpuMillicores ?? 0n)}
                quota={Number(division.quota?.cpuMillicores ?? 0n)}
              />
            </td>
            <td className="px-5 py-3 font-mono text-xs text-muted">
              {division.namespaces.join(", ") || "—"}
            </td>
          </tr>
        ))}
      </Table>
    </section>
  );
}

function NamespaceTable({ namespaces }: { namespaces: Namespace[] }) {
  if (namespaces.length === 0) {
    return null;
  }

  return (
    <section className="flex flex-col gap-3">
      <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">
        Namespaces
      </h2>
      <Table headers={["Namespace", "Division", "Environment", "Default deny", "Limit range"]}>
        {namespaces.map((namespace) => (
          <tr
            key={namespace.name}
            className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
          >
            <td className="px-5 py-3 font-medium">{namespace.name}</td>
            <td className="px-5 py-3 font-mono text-xs text-accent">
              {namespace.division || "unassigned"}
            </td>
            <td className="px-5 py-3 font-mono text-xs text-muted">
              {namespace.environment || "—"}
            </td>
            <td className="px-5 py-3">
              <Present present={namespace.defaultDenyPresent} />
            </td>
            <td className="px-5 py-3">
              <Present present={namespace.limitRangePresent} />
            </td>
          </tr>
        ))}
      </Table>
    </section>
  );
}

function Table({
  headers,
  children,
}: {
  headers: string[];
  children: React.ReactNode;
}) {
  return (
    <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
      <table className="w-full border-collapse text-sm">
        <thead>
          <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
            {headers.map((header) => (
              <th key={header} className="px-5 py-3 font-semibold">
                {header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
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

function PhaseBadge({ phase }: { phase: Division_Phase }) {
  const style = {
    [Division_Phase.ACTIVE]: { color: "bg-healthy", label: "active" },
    [Division_Phase.PENDING]: { color: "bg-progressing", label: "pending" },
    [Division_Phase.SUSPENDED]: { color: "bg-degraded", label: "suspended" },
    [Division_Phase.TERMINATING]: { color: "bg-degraded", label: "terminating" },
    [Division_Phase.UNSPECIFIED]: { color: "bg-muted", label: "unknown" },
  }[phase];

  return (
    <span className="flex items-center gap-2 font-mono text-xs">
      <span className={`inline-block h-2 w-2 rounded-full ${style.color}`} />
      {style.label}
    </span>
  );
}

function Present({ present }: { present: boolean }) {
  return (
    <span
      className={`font-mono text-xs ${present ? "text-healthy" : "text-degraded"}`}
    >
      {present ? "present" : "missing"}
    </span>
  );
}

function Saturation({ used, quota }: { used: number; quota: number }) {
  if (quota === 0) {
    return <span className="font-mono text-xs text-muted">no quota</span>;
  }

  const ratio = Math.min(1, used / quota);
  const percent = Math.round(ratio * 100);
  const tone =
    percent >= 85 ? "bg-degraded" : percent >= 60 ? "bg-progressing" : "bg-healthy";

  return (
    <span className="flex items-center gap-3">
      <span className="h-1.5 w-24 overflow-hidden rounded-full bg-white/10">
        <span
          className={`block h-full ${tone}`}
          style={{ width: `${Math.max(percent, 2)}%` }}
        />
      </span>
      <span className="font-mono text-xs text-muted">{percent}%</span>
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
    <div className="rounded-xl border border-degraded/40 bg-degraded/10 px-4 py-3 font-mono text-xs text-degraded">
      {freshness.degraded.map((source) => (
        <p key={source.source}>
          {source.source} is degraded: {source.reason || "no detail reported"}
        </p>
      ))}
    </div>
  );
}

function EmptyPanel({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="rounded-2xl border border-edge bg-surface px-6 py-10 text-center">
      <p className="text-sm">{title}</p>
      <p className="mt-2 font-mono text-xs text-muted">{hint}</p>
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

function formatCompute(cpuMillicores?: bigint, memoryBytes?: bigint) {
  const cpu = Number(cpuMillicores ?? 0n);
  const memory = Number(memoryBytes ?? 0n);

  if (cpu === 0 && memory === 0) {
    return "none set";
  }

  const cpuLabel = cpu >= 1000 ? `${(cpu / 1000).toFixed(2)} cores` : `${cpu}m`;
  const memoryLabel =
    memory >= 1024 ** 3
      ? `${(memory / 1024 ** 3).toFixed(1)}Gi`
      : `${(memory / 1024 ** 2).toFixed(0)}Mi`;

  return `${cpuLabel} · ${memoryLabel}`;
}
