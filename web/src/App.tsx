import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { catalog } from "./client";

import { ConnectError } from "@connectrpc/connect";

import { useStreamState, type StreamState } from "./events";
import { useSession } from "./useSession";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";
import { Session_AuthMode } from "./gen/marstack/govern/v1/identity_pb";
import { useLiveWorkloads } from "./useLiveWorkloads";
import { useLiveDivisions } from "./useLiveDivisions";
import { RequestsView } from "./RequestsView";
import { CostView } from "./CostView";
import { AuditView } from "./AuditView";
import { PolicyView } from "./PolicyView";
import { TopologyView } from "./TopologyView";
import { EnvironmentsView } from "./EnvironmentsView";
import { DeployView } from "./DeployView";
import type { Workload } from "./gen/marstack/govern/v1/catalog_pb";
import { Workload_Health } from "./gen/marstack/govern/v1/catalog_pb";
import type { Freshness } from "./gen/marstack/govern/v1/common_pb";
import type { Division, Namespace } from "./gen/marstack/govern/v1/tenancy_pb";
import { Division_Phase } from "./gen/marstack/govern/v1/tenancy_pb";

type View = "services" | "divisions" | "requests" | "cost" | "policies" | "topology" | "environments" | "deploy" | "audit";

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
  {
    id: "requests",
    label: "Requests",
    subtitle: "numbers proposed from usage, decided against evidence",
  },
  {
    id: "cost",
    label: "Cost",
    subtitle: "reserved capacity at signed rates, with the idle beside it",
  },
  {
    id: "policies",
    label: "Policies",
    subtitle: "the guardrails in force, and what is failing them",
  },
  {
    id: "topology",
    label: "Topology",
    subtitle: "traced calls beside what the network policies actually permit",
  },
  {
    id: "environments",
    label: "Environments",
    subtitle: "previews that hold a namespace only as long as their lease",
  },
  {
    id: "deploy",
    label: "Deploy",
    subtitle: "curated templates, checked against admission before a merge request is opened",
  },
  {
    id: "audit",
    label: "Audit",
    subtitle: "every mutation, chained so that a rewrite cannot hide",
  },
];

export function App() {
  const [view, setView] = useState<View>("services");
  const streamState = useStreamState();
  const { session, isLoading, switchDivision, signOut } = useSession();
  const active = views.find((candidate) => candidate.id === view) ?? views[0];

  if (isLoading) {
    return (
      <div className="flex min-h-full items-center justify-center bg-canvas">
        <p className="font-mono text-xs text-muted">checking who you are…</p>
      </div>
    );
  }

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
          <div className="ml-auto flex items-center gap-5">
            <StreamBadge state={streamState} />
            <DivisionPicker session={session} onSwitch={switchDivision} />
            <Identity session={session} onSignOut={signOut} />
          </div>
        </div>
        <DevAuthBanner session={session} />
      </header>

      <main className="px-8 py-6">
        {view === "services" ? (
          <ServicesView />
        ) : view === "divisions" ? (
          <DivisionsView />
        ) : view === "requests" ? (
          <RequestsView session={session} />
        ) : view === "cost" ? (
          <CostView session={session} />
        ) : view === "policies" ? (
          <PolicyView session={session} />
        ) : view === "topology" ? (
          <TopologyView session={session} />
        ) : view === "environments" ? (
          <EnvironmentsView session={session} />
        ) : view === "deploy" ? (
          <DeployView session={session} />
        ) : (
          <AuditView />
        )}
      </main>
    </div>
  );
}

function DivisionPicker({
  session,
  onSwitch,
}: {
  session?: Session;
  onSwitch: (division: string) => void;
}) {
  const memberships = session?.memberships ?? [];

  if (memberships.length === 0) {
    return <span className="font-mono text-[11px] text-muted">no divisions</span>;
  }

  return (
    <label className="flex items-center gap-2 font-mono text-[11px] text-muted">
      division
      <select
        value={session?.activeDivision ?? memberships[0].division}
        onChange={(event) => onSwitch(event.target.value)}
        className="rounded-lg border border-edge bg-surface px-2 py-1 text-ink"
      >
        {memberships.map((membership) => (
          <option key={membership.division} value={membership.division}>
            {membership.displayName || membership.division}
          </option>
        ))}
      </select>
    </label>
  );
}

function Identity({
  session,
  onSignOut,
}: {
  session?: Session;
  onSignOut: () => void;
}) {
  if (!session?.actor) {
    return null;
  }

  return (
    <span className="flex items-center gap-3 font-mono text-[11px] text-muted">
      {session.actor.subject}
      {session.platformApprover ? (
        <span className="rounded-md border border-accent/50 px-1.5 py-0.5 text-accent">
          approver
        </span>
      ) : null}
      <button
        type="button"
        onClick={onSignOut}
        className="rounded-lg border border-edge px-2 py-1 text-ink hover:border-primary"
      >
        sign out
      </button>
    </span>
  );
}

function DevAuthBanner({ session }: { session?: Session }) {
  if (session?.authMode !== Session_AuthMode.DEV) {
    return null;
  }

  return (
    <div className="mt-4 rounded-xl border border-progressing/40 bg-progressing/10 px-4 py-2 font-mono text-xs text-progressing">
      authentication is disabled: everyone reaching this page is signed in as{" "}
      {session.actor?.subject}
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
  const [inspected, setInspected] = useState<string | null>(null);
  const [diagnosed, setDiagnosed] = useState<string | null>(null);

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
        "",
      ]}
    >
      {workloads.flatMap((workload) => [
        <tr
          key={workload.uid}
          className="border-b border-edge/60 hover:bg-white/[0.03]"
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
          <td className="px-5 py-3">
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setInspected(inspected === workload.uid ? null : workload.uid)}
                className="rounded-lg border border-edge px-2 py-1 font-mono text-[11px] text-muted hover:border-primary hover:text-ink"
              >
                {inspected === workload.uid ? "hide" : "provenance"}
              </button>
              <button
                type="button"
                onClick={() => setDiagnosed(diagnosed === workload.uid ? null : workload.uid)}
                className={`rounded-lg border px-2 py-1 font-mono text-[11px] hover:border-primary hover:text-ink ${
                  workload.health === Workload_Health.DEGRADED
                    ? "border-degraded/60 text-degraded"
                    : "border-edge text-muted"
                }`}
              >
                {diagnosed === workload.uid ? "hide" : "diagnose"}
              </button>
            </div>
          </td>
        </tr>,
        inspected === workload.uid ? (
          <tr key={`${workload.uid}-provenance`} className="border-b border-edge/60 bg-canvas">
            <td colSpan={9} className="px-5 py-4">
              <ProvenancePanel uid={workload.uid} />
            </td>
          </tr>
        ) : null,
        diagnosed === workload.uid ? (
          <tr key={`${workload.uid}-diagnose`} className="border-b border-edge/60 bg-canvas">
            <td colSpan={9} className="px-5 py-4">
              <DiagnosePanel uid={workload.uid} />
            </td>
          </tr>
        ) : null,
      ])}
    </Table>
  );
}

function DiagnosePanel({ uid }: { uid: string }) {
  const explanation = useQuery({
    queryKey: ["explain", uid],
    queryFn: () => catalog.explainFailure({ uid }),
    retry: false,
  });

  const timeline = useQuery({
    queryKey: ["timeline", uid],
    queryFn: () => catalog.getTimeline({ uid }),
    retry: false,
  });

  const drift = useQuery({
    queryKey: ["drift", uid],
    queryFn: () => catalog.getDrift({ uid }),
    retry: false,
  });

  if (explanation.error) {
    return (
      <p className="font-mono text-xs text-progressing">
        {ConnectError.from(explanation.error).message}
      </p>
    );
  }

  const found = explanation.data?.explanation;
  if (!found) {
    return <p className="font-mono text-xs text-muted">reading the pods…</p>;
  }

  const regression = found.regression;

  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm">{found.cause}</p>
      <p className="font-mono text-xs text-muted">{found.evidence}</p>

      {regression ? (
        <p
          className={`font-mono text-xs ${
            regression.latencyP99After > regression.latencyP99Before * 1.2
              ? "text-degraded"
              : "text-muted"
          }`}
        >
          revision {regression.rolloutRevision} rolled out{" "}
          {regression.rolloutAt
            ? new Date(Number(regression.rolloutAt.seconds) * 1000).toLocaleString()
            : "recently"}
          : p99 {(regression.latencyP99Before * 1000).toFixed(0)}ms →{" "}
          {(regression.latencyP99After * 1000).toFixed(0)}ms · errors{" "}
          {(regression.errorRatioBefore * 100).toFixed(2)}% →{" "}
          {(regression.errorRatioAfter * 100).toFixed(2)}%
        </p>
      ) : null}

      {drift.error ? (
        <p className="font-mono text-xs text-muted">
          {ConnectError.from(drift.error).message}
        </p>
      ) : drift.data?.drifted ? (
        <div className="flex flex-col gap-1">
          <p className="font-mono text-xs text-degraded">
            drifted from {drift.data.application}
          </p>
          {drift.data.fields.map((field, index) => (
            <p key={index} className="font-mono text-xs text-muted">
              {field.path}: desired {field.desired}, live {field.live}
              {field.changedBy ? ` · last changed by ${field.changedBy}` : ""}
            </p>
          ))}
        </div>
      ) : drift.data ? (
        <p className="font-mono text-xs text-healthy">
          matches {drift.data.application}
        </p>
      ) : null}

      {found.reproduceCommands.length > 0 ? (
        <pre className="overflow-auto rounded-xl border border-edge bg-surface p-4 font-mono text-[11px] text-muted">
          {found.reproduceCommands.join("\n")}
        </pre>
      ) : null}

      {(timeline.data?.events ?? []).length > 0 ? (
        <div className="flex flex-col gap-1">
          <p className="font-mono text-[11px] uppercase tracking-wider text-muted">timeline</p>
          {(timeline.data?.events ?? []).slice(0, 8).map((event, index) => (
            <p key={index} className="font-mono text-xs text-muted">
              {event.occurredAt
                ? new Date(Number(event.occurredAt.seconds) * 1000).toLocaleString()
                : "—"}{" "}
              · {event.summary}
            </p>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function ProvenancePanel({ uid }: { uid: string }) {
  const query = useQuery({
    queryKey: ["provenance", uid],
    queryFn: () => catalog.getProvenance({ uid }),
    retry: false,
  });

  if (query.error) {
    return (
      <p className="font-mono text-xs text-progressing">
        {ConnectError.from(query.error).message}
      </p>
    );
  }

  const provenance = query.data?.provenance;
  if (!provenance) {
    return <p className="font-mono text-xs text-muted">asking the registry…</p>;
  }

  const vulnerabilities = provenance.vulnerabilities;

  return (
    <div className="flex flex-col gap-2 font-mono text-xs">
      <p className={provenance.signed ? "text-healthy" : "text-degraded"}>
        {provenance.signed ? "signed with cosign" : "no cosign signature found"}
        {provenance.tagMutable ? " · the tag can move under you" : " · pinned tag"}
        {provenance.sbomPresent ? " · sbom present" : ""}
      </p>

      <p className="text-muted">
        digest {provenance.imageDigest || "unknown"}
        {provenance.baseImageAgeDays > 0 ? ` · built ${provenance.baseImageAgeDays} days ago` : ""}
      </p>

      <p className="text-muted">
        {provenance.sourceRepo
          ? `source ${provenance.sourceRepo}${
              provenance.sourceRevision ? ` at ${provenance.sourceRevision}` : ""
            }`
          : "the image carries no source label, so its origin is unproven"}
      </p>

      {vulnerabilities ? (
        <p className={vulnerabilities.critical > 0 ? "text-degraded" : "text-muted"}>
          {vulnerabilities.critical} critical · {vulnerabilities.high} high ·{" "}
          {vulnerabilities.medium} medium · {vulnerabilities.low} low
        </p>
      ) : (
        <p className="text-muted">no vulnerability report for this digest</p>
      )}

      {provenance.signatureIssuer ? (
        <p className="text-progressing">{provenance.signatureIssuer}</p>
      ) : null}
    </div>
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
