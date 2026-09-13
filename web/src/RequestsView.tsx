import { useEffect, useState } from "react";
import { ConnectError } from "@connectrpc/connect";

import { useRecommendation, useRequests, useSimulation } from "./useRequests";
import { Decision_Outcome } from "./gen/marstack/govern/v1/decisions_pb";
import type { ResourceRequest } from "./gen/marstack/govern/v1/requests_pb";
import { ResourceRequest_Phase } from "./gen/marstack/govern/v1/requests_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";
import { tableHead, tableRow, tableWrap } from "./ui";

const gibibyte = 1024n * 1024n * 1024n;

export function RequestsView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";
  const { requests, queue, isLoading, error, submit, decide, withdraw } =
    useRequests(division || undefined);

  if (error) {
    return (
      <div className="rounded-lg border-l-4 border-degraded bg-degraded-soft px-6 py-6 font-mono text-xs text-degraded">
        {ConnectError.from(error).message}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-8">
      <QuotaForm
        division={division}
        onSubmit={(target, reason) => submit.mutate({ division, target, reason })}
        pending={submit.isPending}
        failure={submit.error}
      />

      {session?.platformApprover && queue.length > 0 ? (
        <ApprovalQueue
          queue={queue}
          onDecide={(request, outcome, reason) =>
            decide.mutate({
              requestUid: request.uid,
              outcome,
              reason,
              evidenceDigest: request.evidenceDigest,
              grantDuration: "2160h",
            })
          }
          pending={decide.isPending}
          failure={decide.error}
        />
      ) : null}

      <RequestTable
        requests={requests}
        isLoading={isLoading}
        onWithdraw={(uid) => withdraw.mutate(uid)}
      />
    </div>
  );
}

function QuotaForm({
  division,
  onSubmit,
  pending,
  failure,
}: {
  division: string;
  onSubmit: (target: { cpuMillicores: bigint; memoryBytes: bigint }, reason: string) => void;
  pending: boolean;
  failure: Error | null;
}) {
  const recommendation = useRecommendation(division || undefined);
  const proposed = recommendation.data?.recommendation?.proposed;

  const [cpu, setCpu] = useState("");
  const [memory, setMemory] = useState("");
  const [reason, setReason] = useState("");

  useEffect(() => {
    if (!proposed) {
      return;
    }
    setCpu((Number(proposed.cpuMillicores) / 1000).toString());
    setMemory((Number(proposed.memoryBytes) / Number(gibibyte)).toString());
  }, [proposed]);

  const exhaustion = recommendation.data?.recommendation?.exhaustionAt;
  const unavailable = recommendation.error
    ? ConnectError.from(recommendation.error).message
    : null;

  if (!division) {
    return (
      <section className="card px-6 py-6">
        <p className="font-mono text-xs text-muted">
          you are not a member of any division yet, so there is nothing to request for
        </p>
      </section>
    );
  }

  return (
    <section className="flex flex-col gap-4 card px-6 py-6">
      <header className="flex flex-wrap items-baseline gap-3">
        <h2 className="text-base font-medium">Ask for more quota</h2>
        <p className="font-mono text-xs text-muted">
          {unavailable
            ? `no proposed number: ${unavailable}`
            : recommendation.data?.recommendation?.basis ?? "reading your usage…"}
        </p>
      </header>

      {exhaustion ? (
        <p className="font-mono text-xs text-progressing">
          at the current rate this division runs out of quota on{" "}
          {new Date(Number(exhaustion.seconds) * 1000).toDateString()}
        </p>
      ) : null}

      <div className="flex flex-wrap gap-4">
        <Field label="cpu (cores)" value={cpu} onChange={setCpu} />
        <Field label="memory (Gi)" value={memory} onChange={setMemory} />
        <label className="flex min-w-0 sm:min-w-[20rem] flex-1 flex-col gap-1 font-mono text-[11px] text-muted">
          reason
          <input
            value={reason}
            onChange={(event) => setReason(event.target.value)}
            placeholder="onboarding two backends for the quarter"
            className="rounded-lg border border-edge bg-raised px-3 py-2 text-sm text-ink"
          />
        </label>
      </div>

      {failure ? (
        <p className="font-mono text-xs text-degraded">{ConnectError.from(failure).message}</p>
      ) : null}

      <div>
        <button
          type="button"
          disabled={pending || !cpu || !memory || reason.trim().length < 10}
          onClick={() =>
            onSubmit(
              {
                cpuMillicores: BigInt(Math.round(Number(cpu) * 1000)),
                memoryBytes: BigInt(Math.round(Number(memory))) * gibibyte,
              },
              reason,
            )
          }
          className="rounded-lg border border-primary px-4 py-2 font-mono text-xs text-ink disabled:border-edge disabled:text-muted"
        >
          {pending ? "filing…" : "file the request"}
        </button>
      </div>
    </section>
  );
}

function Field({
  label,
  value,
  onChange,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <label className="flex w-40 flex-col gap-1 font-mono text-[11px] text-muted">
      {label}
      <input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className="rounded-lg border border-edge bg-raised px-3 py-2 text-sm text-ink"
      />
    </label>
  );
}

function ApprovalQueue({
  queue,
  onDecide,
  pending,
  failure,
}: {
  queue: ResourceRequest[];
  onDecide: (request: ResourceRequest, outcome: Decision_Outcome, reason: string) => void;
  pending: boolean;
  failure: Error | null;
}) {
  const [reasons, setReasons] = useState<Record<string, string>>({});

  return (
    <section className="flex flex-col gap-3">
      <h2 className="card-title">
        Waiting for a decision
      </h2>

      {failure ? (
        <p className="font-mono text-xs text-degraded">{ConnectError.from(failure).message}</p>
      ) : null}

      <div className="flex flex-col gap-4">
        {queue.map((request) => (
          <article
            key={request.uid}
            className="flex flex-col gap-3 card px-5 py-4"
          >
            <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
              <span className="font-mono text-xs text-accent">{request.division}</span>
              <span className="text-sm font-medium">
                {formatCompute(request.quota?.target?.cpuMillicores, request.quota?.target?.memoryBytes)}
              </span>
              <span className="font-mono text-xs text-muted">
                from {request.requester?.subject}
              </span>
            </div>

            <p className="text-sm text-muted">{request.reason}</p>

            <Evidence request={request} />

            <div className="flex flex-wrap items-center gap-3">
              <input
                value={reasons[request.uid] ?? ""}
                onChange={(event) =>
                  setReasons((current) => ({ ...current, [request.uid]: event.target.value }))
                }
                placeholder="the reason for your decision, recorded with it"
                className="min-w-0 sm:min-w-[22rem] flex-1 rounded-lg border border-edge bg-raised px-3 py-2 text-sm text-ink"
              />
              <button
                type="button"
                disabled={pending || (reasons[request.uid] ?? "").trim().length < 10}
                onClick={() =>
                  onDecide(request, Decision_Outcome.APPROVED, reasons[request.uid] ?? "")
                }
                className="rounded-lg border border-healthy px-3 py-2 font-mono text-xs text-healthy disabled:border-edge disabled:text-muted"
              >
                approve for 90 days
              </button>
              <button
                type="button"
                disabled={pending || (reasons[request.uid] ?? "").trim().length < 10}
                onClick={() =>
                  onDecide(request, Decision_Outcome.REJECTED, reasons[request.uid] ?? "")
                }
                className="rounded-lg border border-degraded px-3 py-2 font-mono text-xs text-degraded disabled:border-edge disabled:text-muted"
              >
                reject
              </button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}

function Evidence({ request }: { request: ResourceRequest }) {
  const recommendation = request.recommendation;
  const preflight = request.preflight;

  return (
    <div className="flex flex-col gap-2 rounded-xl border border-edge/70 bg-raised px-4 py-3">
      <p className="card-title">evidence</p>

      {recommendation ? (
        <p className="font-mono text-xs text-muted">
          observed p99 {formatCompute(recommendation.observedP99?.cpuMillicores, recommendation.observedP99?.memoryBytes)}
          {" · "}proposed {formatCompute(recommendation.proposed?.cpuMillicores, recommendation.proposed?.memoryBytes)}
          {" · "}{recommendation.basis}
        </p>
      ) : (
        <p className="font-mono text-xs text-progressing">
          no usage was observed, so nothing is proposed
        </p>
      )}

      <SimulationLine requestUid={request.uid} />

      {preflight ? (
        <div className="flex flex-col gap-1">
          <p className={`font-mono text-xs ${preflight.admitted ? "text-healthy" : "text-degraded"}`}>
            preflight {preflight.admitted ? "admits" : "blocks"} this target · cluster would be{" "}
            {Math.round(preflight.quotaUtilisationAfter * 100)}% committed
          </p>
          {preflight.rejections.map((rejection, index) => (
            <p key={index} className="font-mono text-xs text-muted">
              {rejection.severity === 2 ? "blocked" : "warning"}: {rejection.message}
            </p>
          ))}
        </div>
      ) : null}

      <p className="font-mono text-[10px] text-muted">
        digest {request.evidenceDigest.slice(0, 12) || "none"}
      </p>
    </div>
  );
}

function SimulationLine({ requestUid }: { requestUid: string }) {
  const simulation = useSimulation(requestUid);

  if (simulation.error) {
    return (
      <p className="font-mono text-xs text-progressing">
        no simulation: {ConnectError.from(simulation.error).message}
      </p>
    );
  }

  const impact = simulation.data?.simulation;
  if (!impact) {
    return <p className="font-mono text-xs text-muted">packing the cluster…</p>;
  }

  const nodes = impact.nodesNoLongerFitting.map((node) => node.name);

  return (
    <div className="flex flex-col gap-1">
      <p className={`font-mono text-xs ${impact.schedulable ? "text-healthy" : "text-degraded"}`}>
        simulation: {impact.verdict}
      </p>
      <p className="font-mono text-xs text-muted">
        cluster commitment {Math.round(impact.clusterCommitmentBefore * 100)}% →{" "}
        {Math.round(impact.clusterCommitmentAfter * 100)}%
        {nodes.length > 0 ? ` · nodes that fill up: ${nodes.join(", ")}` : ""}
      </p>
      {impact.podsAtRisk.slice(0, 3).map((pod, index) => (
        <p key={index} className="font-mono text-xs text-degraded">
          at risk: {pod.namespace}/{pod.name} — {pod.reason}
        </p>
      ))}
    </div>
  );
}

function RequestTable({
  requests,
  isLoading,
  onWithdraw,
}: {
  requests: ResourceRequest[];
  isLoading: boolean;
  onWithdraw: (uid: string) => void;
}) {
  if (isLoading) {
    return <p className="font-mono text-xs text-muted">loading the projection…</p>;
  }

  if (requests.length === 0) {
    return (
      <div className="card text-center">
        <p className="text-sm">No requests have been filed.</p>
      </div>
    );
  }

  return (
    <section className="flex flex-col gap-3">
      <h2 className="card-title">All requests</h2>
      <div className={tableWrap}>
        <table className="ms-table">
          <thead>
            <tr className={tableHead}>
              {["Phase", "Division", "Target", "Requester", "Reason", "Decision", ""].map((header) => (
                <th key={header} className="font-semibold">
                  {header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {requests.map((request) => (
              <tr
                key={request.uid}
                className={tableRow}
              >
                <td >
                  <PhaseLabel phase={request.phase} />
                </td>
                <td className="font-mono text-xs text-accent">{request.division}</td>
                <td className="font-mono text-xs">
                  {formatCompute(request.quota?.target?.cpuMillicores, request.quota?.target?.memoryBytes)}
                </td>
                <td className="font-mono text-xs text-muted">
                  {request.requester?.subject}
                </td>
                <td className="max-w-[20rem] truncate px-5 py-3 text-muted">{request.reason}</td>
                <td className="font-mono text-xs text-muted">
                  {request.decided
                    ? `${request.decided.outcome} by ${request.decided.decider}`
                    : "—"}
                </td>
                <td >
                  {isOpen(request.phase) ? (
                    <button
                      type="button"
                      onClick={() => onWithdraw(request.uid)}
                      className="rounded-lg border border-edge px-2 py-1 font-mono text-[11px] text-muted hover:border-primary hover:text-ink"
                    >
                      withdraw
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function PhaseLabel({ phase }: { phase: ResourceRequest_Phase }) {
  const style: Record<number, { color: string; label: string }> = {
    [ResourceRequest_Phase.PENDING]: { color: "text-progressing", label: "pending" },
    [ResourceRequest_Phase.AWAITING_DECISION]: { color: "text-progressing", label: "awaiting" },
    [ResourceRequest_Phase.APPROVED]: { color: "text-healthy", label: "approved" },
    [ResourceRequest_Phase.APPLIED]: { color: "text-healthy", label: "applied" },
    [ResourceRequest_Phase.REJECTED]: { color: "text-degraded", label: "rejected" },
    [ResourceRequest_Phase.CHANGES_REQUESTED]: { color: "text-progressing", label: "changes" },
    [ResourceRequest_Phase.EXPIRED]: { color: "text-muted", label: "expired" },
    [ResourceRequest_Phase.WITHDRAWN]: { color: "text-muted", label: "withdrawn" },
    [ResourceRequest_Phase.PREFLIGHT_FAILED]: { color: "text-degraded", label: "blocked" },
    [ResourceRequest_Phase.UNSPECIFIED]: { color: "text-muted", label: "unknown" },
  };

  const chosen = style[phase] ?? style[ResourceRequest_Phase.UNSPECIFIED];

  return <span className={`font-mono text-xs ${chosen.color}`}>{chosen.label}</span>;
}

function isOpen(phase: ResourceRequest_Phase) {
  return (
    phase === ResourceRequest_Phase.PENDING ||
    phase === ResourceRequest_Phase.AWAITING_DECISION ||
    phase === ResourceRequest_Phase.CHANGES_REQUESTED
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
