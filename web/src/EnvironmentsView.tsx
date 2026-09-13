import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { environments } from "./client";
import { badge, tableHead, tableRow, tableWrap } from "./ui";
import { PreviewEnvironment_Phase } from "./gen/marstack/govern/v1/environments_pb";
import type { PreviewEnvironment } from "./gen/marstack/govern/v1/environments_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export function EnvironmentsView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";
  const [includeReclaimed, setIncludeReclaimed] = useState(false);
  const [renewing, setRenewing] = useState<string>("");
  const [reason, setReason] = useState("");
  const queryClient = useQueryClient();

  const previews = useQuery({
    queryKey: ["environments", division, includeReclaimed],
    queryFn: () => environments.listEnvironments({ division, includeReclaimed }),
    enabled: Boolean(division),
    retry: false,
  });

  const renew = useMutation({
    mutationFn: (input: { name: string; extend: string; reason: string }) =>
      environments.renewEnvironment(input),
    onSuccess: () => {
      setRenewing("");
      setReason("");
      void queryClient.invalidateQueries({ queryKey: ["environments"] });
    },
  });

  const release = useMutation({
    mutationFn: (name: string) => environments.releaseEnvironment({ name }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["environments"] }),
  });

  if (!division) {
    return (
      <div className="card text-center">
        <p className="text-sm">Pick a division to see the previews it is holding open.</p>
      </div>
    );
  }

  if (previews.error) {
    return (
      <div className="rounded-lg border-l-4 border-progressing bg-progressing-soft px-6 py-6 font-mono text-xs text-progressing">
        {ConnectError.from(previews.error).message}
      </div>
    );
  }

  const rows = previews.data?.environments ?? [];
  const live = rows.filter((row) => row.phase === PreviewEnvironment_Phase.READY);
  const expiringSoon = live.filter((row) => Number(row.remainingSeconds) < 6 * 3600);

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-wrap gap-4">
        <Tile label="previews holding a namespace" value={live.length} tone="text-ink" />
        <Tile label="expiring within six hours" value={expiringSoon.length} tone="text-progressing" />
      </section>

      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <h2 className="card-title">
            Preview environments in {division}
          </h2>
          <label className="flex items-center gap-2 font-mono text-[11px] text-muted">
            <input
              type="checkbox"
              checked={includeReclaimed}
              onChange={(event) => setIncludeReclaimed(event.target.checked)}
            />
            show reclaimed
          </label>
        </div>

        {rows.length === 0 ? (
          <div className="card text-center">
            <p className="text-sm">No preview is holding a namespace right now.</p>
          </div>
        ) : (
          <div className={tableWrap}>
            <table className="ms-table">
              <thead>
                <tr className={tableHead}>
                  {["Phase", "Change", "Namespace", "Lease", "Requester", ""].map((header, index) => (
                    <th key={index} className="font-semibold">
                      {header}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr
                    key={row.name}
                    className={tableRow}
                  >
                    <td >
                      <span className={phaseTone(row.phase)}>
                        {phaseLabel(row.phase)}
                      </span>
                    </td>
                    <td >
                      <span className="font-medium">
                        {row.change?.repository}!{row.change?.number}
                      </span>
                      <span className="ml-2 font-mono text-xs text-muted">
                        {row.change?.branch}
                      </span>
                    </td>
                    <td className="font-mono text-xs text-accent">
                      {row.namespace || "—"}
                    </td>
                    <td >
                      <Lease preview={row} />
                    </td>
                    <td className="font-mono text-xs text-muted">{row.requestedBy}</td>
                    <td className="text-right">
                      {row.phase !== PreviewEnvironment_Phase.READY ? null : renewing === row.name ? (
                        <div className="flex justify-end gap-2">
                          <input
                            autoFocus
                            value={reason}
                            onChange={(event) => setReason(event.target.value)}
                            placeholder="why it needs another day"
                            className="w-64 rounded-lg border border-edge bg-raised px-3 py-1 font-mono text-[11px] text-ink"
                          />
                          <button
                            type="button"
                            disabled={renew.isPending || reason.trim().length < 10}
                            onClick={() => renew.mutate({ name: row.name, extend: "24h", reason })}
                            className="rounded-lg border border-edge px-3 py-1 font-mono text-[11px] text-muted hover:text-ink disabled:opacity-40"
                          >
                            grant +24h
                          </button>
                          <button
                            type="button"
                            onClick={() => setRenewing("")}
                            className="rounded-lg px-2 py-1 font-mono text-[11px] text-muted hover:text-ink"
                          >
                            cancel
                          </button>
                        </div>
                      ) : (
                        <div className="flex justify-end gap-2">
                          <button
                            type="button"
                            onClick={() => {
                              setRenewing(row.name);
                              setReason("");
                            }}
                            className="rounded-lg border border-edge px-3 py-1 font-mono text-[11px] text-muted hover:text-ink"
                          >
                            renew
                          </button>
                          <button
                            type="button"
                            disabled={release.isPending}
                            onClick={() => release.mutate(row.name)}
                            className="rounded-lg border border-edge px-3 py-1 font-mono text-[11px] text-muted hover:text-degraded"
                          >
                            release
                          </button>
                        </div>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {renew.error ? (
          <p className="font-mono text-xs text-degraded">
            {ConnectError.from(renew.error).message}
          </p>
        ) : null}
        {release.error ? (
          <p className="font-mono text-xs text-degraded">
            {ConnectError.from(release.error).message}
          </p>
        ) : null}
      </section>
    </div>
  );
}

function Lease({ preview }: { preview: PreviewEnvironment }) {
  if (preview.phase !== PreviewEnvironment_Phase.READY) {
    const reclaimed = preview.conditions.find((condition) => condition.type === "NamespaceReady");

    return <span className="font-mono text-xs text-muted">{reclaimed?.message ?? "—"}</span>;
  }

  const remaining = Number(preview.remainingSeconds);
  const tone = remaining < 6 * 3600 ? "text-progressing" : "text-muted";

  return (
    <span className={`font-mono text-xs ${tone}`}>
      {formatRemaining(remaining)} left of {preview.grantedTtl}
      {preview.renewals.length > 0 ? ` · renewed ${preview.renewals.length}×` : ""}
    </span>
  );
}

function Tile({ label, value, tone }: { label: string; value: number; tone: string }) {
  return (
    <div className="flex min-w-0 sm:min-w-[13rem] flex-1 items-center gap-4 rounded-lg bg-surface px-5 py-4 shadow-[var(--shadow-card)]">
      <span className={`h-10 w-1 shrink-0 rounded-full ${tone.replace("text-", "bg-")}`} />
      <span className="flex flex-col gap-0.5">
        <span className={`text-2xl leading-none font-medium ${tone}`}>{value}</span>
        <span className="text-[11px] uppercase tracking-wider text-faint">{label}</span>
      </span>
    </div>
  );
}

function formatRemaining(seconds: number) {
  if (seconds <= 0) {
    return "none";
  }

  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);

  return hours > 0 ? `${hours}h ${minutes}m` : `${minutes}m`;
}

function phaseLabel(phase: PreviewEnvironment_Phase) {
  switch (phase) {
    case PreviewEnvironment_Phase.READY:
      return "ready";
    case PreviewEnvironment_Phase.RECLAIMING:
      return "reclaiming";
    case PreviewEnvironment_Phase.EXPIRED:
      return "reclaimed";
    case PreviewEnvironment_Phase.ORPHANED:
      return "orphaned";
    default:
      return "pending";
  }
}

function phaseTone(phase: PreviewEnvironment_Phase) {
  switch (phase) {
    case PreviewEnvironment_Phase.READY:
      return badge("healthy");
    case PreviewEnvironment_Phase.RECLAIMING:
      return badge("progressing");
    case PreviewEnvironment_Phase.ORPHANED:
      return badge("degraded");
    default:
      return badge("neutral");
  }
}
