import { useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { topology } from "./client";
import { NetworkPath_Verdict } from "./gen/marstack/govern/v1/topology_pb";
import type { NetworkPath } from "./gen/marstack/govern/v1/topology_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export function TopologyView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";

  const graph = useQuery({
    queryKey: ["service-graph", division],
    queryFn: () => topology.getServiceGraph({ division }),
    enabled: Boolean(division),
    retry: false,
  });

  const reachability = useQuery({
    queryKey: ["reachability", division],
    queryFn: () => topology.getReachability({ division }),
    enabled: Boolean(division),
    retry: false,
  });

  if (!division) {
    return (
      <div className="rounded-2xl border border-edge bg-surface px-6 py-10 text-center">
        <p className="text-sm">Pick a division to see how its services talk to each other.</p>
      </div>
    );
  }

  const edges = graph.data?.edges ?? [];
  const paths = reachability.data?.paths ?? [];
  const unused = paths.filter((path) => path.verdict === NetworkPath_Verdict.ALLOWED_UNUSED);
  const undeclared = paths.filter(
    (path) => path.verdict === NetworkPath_Verdict.OBSERVED_NOT_ALLOWED,
  );

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-col gap-3">
        <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">
          Who calls whom, from trace metrics
        </h2>

        {graph.error ? (
          <div className="rounded-2xl border border-progressing/40 bg-progressing/10 px-6 py-6 font-mono text-xs text-progressing">
            {ConnectError.from(graph.error).message}
          </div>
        ) : edges.length === 0 ? (
          <div className="rounded-2xl border border-edge bg-surface px-6 py-10 text-center">
            <p className="text-sm">No calls were traced in this window.</p>
          </div>
        ) : (
          <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
                  {["Caller", "Callee", "Requests/s", "Errors"].map((header) => (
                    <th key={header} className="px-5 py-3 font-semibold">
                      {header}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {edges.map((edge) => (
                  <tr
                    key={`${edge.client}->${edge.server}`}
                    className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
                  >
                    <td className="px-5 py-3 font-medium">{edge.client}</td>
                    <td className="px-5 py-3 font-medium">{edge.server}</td>
                    <td className="px-5 py-3 font-mono text-xs text-muted">
                      {edge.requestsPerSecond.toFixed(2)}
                    </td>
                    <td className="px-5 py-3">
                      <span
                        className={`font-mono text-xs ${
                          edge.errorRatio > 0.05
                            ? "text-degraded"
                            : edge.errorRatio > 0
                              ? "text-progressing"
                              : "text-healthy"
                        }`}
                      >
                        {(edge.errorRatio * 100).toFixed(1)}%
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">
          What the network policies actually permit
        </h2>

        {reachability.error ? (
          <p className="font-mono text-xs text-progressing">
            {ConnectError.from(reachability.error).message}
          </p>
        ) : (
          <>
            {reachability.data && !reachability.data.trafficObserved ? (
              <p className="font-mono text-xs text-muted">
                No traffic could be observed, so every path below is read from policy alone.
              </p>
            ) : null}

            <div className="flex flex-wrap gap-4">
              <Tile
                label="allowances nobody uses"
                value={unused.length}
                tone="text-progressing"
              />
              <Tile
                label="traffic no policy permits"
                value={undeclared.length}
                tone="text-degraded"
              />
            </div>

            <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
              <table className="w-full border-collapse text-sm">
                <thead>
                  <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
                    {["From", "To", "Verdict", "Allowed by"].map((header) => (
                      <th key={header} className="px-5 py-3 font-semibold">
                        {header}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {paths.map((path) => (
                    <tr
                      key={`${path.from}->${path.to}`}
                      className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
                    >
                      <td className="px-5 py-3 font-mono text-xs text-muted">{path.from}</td>
                      <td className="px-5 py-3 font-medium">{path.to}</td>
                      <td className="px-5 py-3">
                        <span className={`font-mono text-xs ${verdictTone(path)}`}>
                          {verdictLabel(path)}
                        </span>
                      </td>
                      <td className="px-5 py-3 font-mono text-xs text-accent">
                        {path.allowedBy || "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </section>
    </div>
  );
}

function Tile({ label, value, tone }: { label: string; value: number; tone: string }) {
  return (
    <div className="flex min-w-[14rem] flex-1 flex-col gap-2 rounded-2xl border border-edge bg-surface px-5 py-4">
      <span className="font-mono text-[11px] uppercase tracking-wider text-muted">{label}</span>
      <span className={`font-mono text-2xl ${tone}`}>{value}</span>
    </div>
  );
}

function verdictLabel(path: NetworkPath) {
  switch (path.verdict) {
    case NetworkPath_Verdict.ALLOWED_AND_USED:
      return "allowed and used";
    case NetworkPath_Verdict.ALLOWED_UNUSED:
      return "allowed, never observed";
    case NetworkPath_Verdict.OBSERVED_NOT_ALLOWED:
      return "observed, nothing allows it";
    default:
      return "denied";
  }
}

function verdictTone(path: NetworkPath) {
  switch (path.verdict) {
    case NetworkPath_Verdict.ALLOWED_AND_USED:
      return "text-healthy";
    case NetworkPath_Verdict.ALLOWED_UNUSED:
      return "text-progressing";
    case NetworkPath_Verdict.OBSERVED_NOT_ALLOWED:
      return "text-degraded";
    default:
      return "text-muted";
  }
}
