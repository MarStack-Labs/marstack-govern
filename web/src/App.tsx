import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { catalog } from "./client";

import { ConnectError } from "@connectrpc/connect";

import { useStreamState, type StreamState } from "./events";
import { badge, tableHead, tableRow, tableWrap, type Tone } from "./ui";
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
  const [drawer, setDrawer] = useState(false);
  const streamState = useStreamState();
  const { session, isLoading, switchDivision, signOut } = useSession();
  const active = views.find((candidate) => candidate.id === view) ?? views[0];

  if (isLoading) {
    return (
      <div className="flex min-h-full items-center justify-center bg-raised">
        <p className="font-mono text-xs text-muted">checking who you are…</p>
      </div>
    );
  }

  return (
    <div className="ms-shell" data-drawer={drawer ? "open" : "closed"}>
      <aside className="ms-rail">
        <a className="ms-rail-brand" href="/">
          <span className="grid size-7 place-items-center rounded-md bg-[var(--ms-accent-solid)] text-[13px] font-bold text-white">
            m
          </span>
          <span className="ms-wordmark">margov</span>
        </a>

        <nav className="flex flex-col gap-0.5">
          <p className="ms-eyebrow px-3 pb-1">Platform</p>
          {views.map((candidate) => (
            <button
              key={candidate.id}
              type="button"
              className="ms-nav-item"
              aria-current={candidate.id === view ? "page" : undefined}
              onClick={() => {
                setView(candidate.id);
                setDrawer(false);
              }}
            >
              {candidate.label}
            </button>
          ))}
        </nav>
      </aside>

      <div className="ms-main">
        <header className="ms-topbar">
          <button
            type="button"
            className="ms-btn ms-drawer-btn"
            aria-label="Toggle navigation"
            aria-expanded={drawer}
            onClick={() => setDrawer((on) => !on)}
          >
            <MenuIcon />
          </button>

          <div className="ms-page-head">
            <h1>{active.label}</h1>
          </div>

          <div className="ml-auto flex flex-wrap items-center gap-4">
            <StreamBadge state={streamState} />
            <DivisionPicker session={session} onSwitch={switchDivision} />
            <Identity session={session} onSignOut={signOut} />
          </div>
        </header>

        <DevAuthBanner session={session} />

        <main className="ms-page">
          <p className="text-sm text-muted">{active.subtitle}</p>

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
        className="rounded-lg border border-edge bg-surface shadow-sm shadow-slate-900/[0.04] px-2 py-1 text-ink"
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
    <div className="mt-4 rounded-xl border border-progressing/30 bg-progressing-soft px-4 py-2 font-mono text-xs text-progressing">
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
          className={tableRow}
        >
          <td >
            <HealthDot health={workload.health} />
          </td>
          <td className="font-mono text-xs text-accent">
            {workload.division || "unassigned"}
          </td>
          <td className="font-mono text-xs text-muted">
            {workload.namespace}
          </td>
          <td className="font-medium">{workload.name}</td>
          <td className="font-mono text-xs text-muted">{workload.kind}</td>
          <td className="font-mono text-xs">
            {workload.replicasReady}/{workload.replicasDesired}
          </td>
          <td className="font-mono text-xs text-muted">
            {formatCompute(
              workload.requested?.cpuMillicores,
              workload.requested?.memoryBytes,
            )}
          </td>
          <td className="max-w-[22rem] truncate px-5 py-3 font-mono text-xs text-muted">
            {workload.imageRef || "—"}
          </td>
          <td >
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
          <tr key={`${workload.uid}-provenance`} className="border-b border-edge/60 bg-primary-soft/30">
            <td colSpan={9} className="px-5 py-4">
              <ProvenancePanel uid={workload.uid} />
            </td>
          </tr>
        ) : null,
        diagnosed === workload.uid ? (
          <tr key={`${workload.uid}-diagnose`} className="border-b border-edge/60 bg-primary-soft/30">
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
        <pre className="overflow-auto rounded-xl border border-edge bg-surface shadow-sm shadow-slate-900/[0.04] p-4 font-mono text-[11px] text-muted">
          {found.reproduceCommands.join("\n")}
        </pre>
      ) : null}

      {(timeline.data?.events ?? []).length > 0 ? (
        <div className="flex flex-col gap-1">
          <p className="card-title">timeline</p>
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
      <h2 className="card-title">
        Divisions
      </h2>
      <Table headers={["Phase", "Name", "Display", "Quota", "Used", "Saturation", "Namespaces"]}>
        {divisions.map((division) => (
          <tr
            key={division.uid}
            className={tableRow}
          >
            <td >
              <PhaseBadge phase={division.phase} />
            </td>
            <td className="font-mono text-xs text-accent">{division.name}</td>
            <td className="font-medium">{division.displayName}</td>
            <td className="font-mono text-xs text-muted">
              {formatCompute(
                division.quota?.cpuMillicores,
                division.quota?.memoryBytes,
              )}
            </td>
            <td className="font-mono text-xs text-muted">
              {formatCompute(division.used?.cpuMillicores, division.used?.memoryBytes)}
            </td>
            <td >
              <Saturation
                used={Number(division.used?.cpuMillicores ?? 0n)}
                quota={Number(division.quota?.cpuMillicores ?? 0n)}
              />
            </td>
            <td className="font-mono text-xs text-muted">
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
      <h2 className="card-title">
        Namespaces
      </h2>
      <Table headers={["Namespace", "Division", "Environment", "Default deny", "Limit range"]}>
        {namespaces.map((namespace) => (
          <tr
            key={namespace.name}
            className={tableRow}
          >
            <td className="font-medium">{namespace.name}</td>
            <td className="font-mono text-xs text-accent">
              {namespace.division || "unassigned"}
            </td>
            <td className="font-mono text-xs text-muted">
              {namespace.environment || "—"}
            </td>
            <td >
              <Present present={namespace.defaultDenyPresent} />
            </td>
            <td >
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
    <div className={tableWrap}>
      <table className="ms-table">
        <thead>
          <tr className={tableHead}>
            {headers.map((header) => (
              <th key={header} className="font-semibold">
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

function MenuIcon() {
  return (
    <svg viewBox="0 0 24 24" className="size-5" fill="none" stroke="currentColor" strokeWidth="2">
      <path d="M3 6h18M3 12h18M3 18h18" strokeLinecap="round" />
    </svg>
  );
}

function HealthDot({ health }: { health: Workload_Health }) {
  const style = {
    [Workload_Health.HEALTHY]: { tone: "healthy" as Tone, label: "healthy" },
    [Workload_Health.PROGRESSING]: { tone: "progressing" as Tone, label: "progressing" },
    [Workload_Health.DEGRADED]: { tone: "degraded" as Tone, label: "degraded" },
    [Workload_Health.UNSPECIFIED]: { tone: "neutral" as Tone, label: "unknown" },
  }[health];

  return (
    <span className={badge(style.tone)}>{style.label}</span>
  );
}

function PhaseBadge({ phase }: { phase: Division_Phase }) {
  const style = {
    [Division_Phase.ACTIVE]: { tone: "healthy" as Tone, label: "active" },
    [Division_Phase.PENDING]: { tone: "progressing" as Tone, label: "pending" },
    [Division_Phase.SUSPENDED]: { tone: "degraded" as Tone, label: "suspended" },
    [Division_Phase.TERMINATING]: { tone: "degraded" as Tone, label: "terminating" },
    [Division_Phase.UNSPECIFIED]: { tone: "neutral" as Tone, label: "unknown" },
  }[phase];

  return (
    <span className={badge(style.tone)}>{style.label}</span>
  );
}

function Present({ present }: { present: boolean }) {
  return (
    <span
      className={badge(present ? "healthy" : "degraded")}
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
      <span className="h-1.5 w-24 overflow-hidden rounded-full bg-primary-soft">
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
    <div className="card text-center">
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
    <div className="rounded-lg border-l-4 border-degraded bg-degraded-soft px-6 py-6">
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
