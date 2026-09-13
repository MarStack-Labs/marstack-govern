import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { auditTrail } from "./client";
import type { AuditEvent } from "./gen/marstack/govern/v1/audit_pb";
import { tableHead, tableRow, tableWrap } from "./ui";

export function AuditView() {
  const [actor, setActor] = useState("");
  const [verb, setVerb] = useState("");

  const events = useQuery({
    queryKey: ["audit", actor, verb],
    queryFn: () => auditTrail.listEvents({ actor, verb, page: { size: 100 } }),
    retry: false,
  });

  const verify = useMutation({
    mutationFn: () => auditTrail.verifyChain({ compareArchive: true }),
  });

  const [expanded, setExpanded] = useState<string | null>(null);

  if (events.error) {
    return (
      <div className="rounded-lg border-l-4 border-degraded bg-degraded-soft px-6 py-6 font-mono text-xs text-degraded">
        {ConnectError.from(events.error).message}
      </div>
    );
  }

  const verification = verify.data?.verification;

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-wrap items-end gap-4">
        <label className="flex w-56 flex-col gap-1 font-mono text-[11px] text-muted">
          actor
          <input
            value={actor}
            onChange={(event) => setActor(event.target.value)}
            placeholder="dev@example.test"
            className="rounded-lg border border-edge bg-raised px-3 py-2 text-sm text-ink"
          />
        </label>
        <label className="flex w-40 flex-col gap-1 font-mono text-[11px] text-muted">
          verb
          <input
            value={verb}
            onChange={(event) => setVerb(event.target.value)}
            placeholder="create"
            className="rounded-lg border border-edge bg-raised px-3 py-2 text-sm text-ink"
          />
        </label>

        <button
          type="button"
          onClick={() => verify.mutate()}
          disabled={verify.isPending}
          className="rounded-lg border border-primary px-4 py-2 font-mono text-xs text-ink disabled:border-edge disabled:text-muted"
        >
          {verify.isPending ? "verifying…" : "verify the chain"}
        </button>

        {verify.error ? (
          <span className="font-mono text-xs text-degraded">
            {ConnectError.from(verify.error).message}
          </span>
        ) : null}

        {verification ? (
          <span
            className={`font-mono text-xs ${verification.intact ? "text-healthy" : "text-degraded"}`}
          >
            {verification.intact
              ? `${verification.checkedEvents} events verify${
                  verification.archiveCompared ? ", archive agrees" : ", database only"
                }`
              : `broken at ${verification.brokenAtSeq}: ${verification.detail}`}
          </span>
        ) : null}
      </section>

      {events.data?.events.length === 0 ? (
        <div className="card text-center">
          <p className="text-sm">Nothing has been recorded yet.</p>
          <p className="mt-2 font-mono text-xs text-muted">
            point the Kubernetes audit webhook at /v1/audit to start the trail
          </p>
        </div>
      ) : (
        <div className={tableWrap}>
          <table className="ms-table">
            <thead>
              <tr className={tableHead}>
                {["Seq", "When", "Actor", "Verb", "Resource", "Object", "Code", ""].map((header) => (
                  <th key={header} className="font-semibold">
                    {header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(events.data?.events ?? []).map((event) => (
                <EventRow
                  key={event.seq.toString()}
                  event={event}
                  expanded={expanded === event.seq.toString()}
                  onToggle={() =>
                    setExpanded(expanded === event.seq.toString() ? null : event.seq.toString())
                  }
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}

function EventRow({
  event,
  expanded,
  onToggle,
}: {
  event: AuditEvent;
  expanded: boolean;
  onToggle: () => void;
}) {
  return (
    <>
      <tr className={tableRow}>
        <td className="font-mono text-xs text-muted">{event.seq.toString()}</td>
        <td className="font-mono text-xs text-muted">
          {event.eventAt ? new Date(Number(event.eventAt.seconds) * 1000).toLocaleString() : "—"}
        </td>
        <td className="font-mono text-xs">
          {event.actor?.subject}
          {event.impersonatedBy ? (
            <span className="text-muted"> via {event.impersonatedBy}</span>
          ) : null}
        </td>
        <td className="font-mono text-xs text-accent">{event.verb}</td>
        <td className="font-mono text-xs text-muted">{event.resource}</td>
        <td className="font-mono text-xs text-muted">
          {event.namespace ? `${event.namespace}/` : ""}
          {event.objectName || "—"}
        </td>
        <td
          className={`px-5 py-3 font-mono text-xs ${
            event.responseCode >= 400 ? "text-degraded" : "text-muted"
          }`}
        >
          {event.responseCode}
        </td>
        <td >
          <button
            type="button"
            onClick={onToggle}
            className="rounded-lg border border-edge px-2 py-1 font-mono text-[11px] text-muted hover:border-primary hover:text-ink"
          >
            {expanded ? "hide" : "payload"}
          </button>
        </td>
      </tr>
      {expanded ? (
        <tr className="border-b border-edge/60 bg-primary-soft/30">
          <td colSpan={8} className="px-5 py-4">
            <p className="mb-2 font-mono text-[10px] text-muted">
              hash {event.hash.slice(0, 16)} · follows {event.prevHash.slice(0, 16) || "the start"}
            </p>
            <pre className="max-h-80 overflow-auto rounded-xl border border-edge bg-surface shadow-sm shadow-slate-900/[0.04] p-4 font-mono text-[11px] text-muted">
              {prettyJson(event.payloadJson)}
            </pre>
          </td>
        </tr>
      ) : null}
    </>
  );
}

function prettyJson(value: string) {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}
