import { useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { finops } from "./client";
import { CostComponent_Resource } from "./gen/marstack/govern/v1/finops_pb";
import type { Money } from "./gen/marstack/govern/v1/common_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export function CostView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";

  const query = useQuery({
    queryKey: ["cost", division],
    queryFn: () => finops.getDivisionCost({ division }),
    enabled: Boolean(division),
    retry: false,
  });

  if (!division) {
    return (
      <p className="font-mono text-xs text-muted">
        you are not a member of any division, so there is no bill to show
      </p>
    );
  }

  if (query.error) {
    return (
      <div className="rounded-2xl border border-degraded/40 bg-degraded/10 px-6 py-6 font-mono text-xs text-degraded">
        {ConnectError.from(query.error).message}
      </div>
    );
  }

  if (!query.data?.cost) {
    return <p className="font-mono text-xs text-muted">adding it up…</p>;
  }

  const cost = query.data.cost;

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-wrap gap-4">
        <Tile label={`charged so far · ${cost.period}`} amount={cost.charged} tone="text-ink" />
        <Tile label="projected month end" amount={cost.projectedMonthEnd} tone="text-primary" />
        <Tile label="idle — reserved, never used" amount={cost.idle} tone="text-progressing" />
      </section>

      <p className="font-mono text-xs text-muted">
        billed on reserved capacity at the rates in {cost.pricingPolicy} revision{" "}
        {cost.pricingPolicyRevision} · usage is shown as idle, not deducted
      </p>

      <section className="flex flex-col gap-3">
        <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">Breakdown</h2>
        <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
          <table className="w-full border-collapse text-sm">
            <thead>
              <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
                {["Resource", "Quantity", "Rate", "Amount"].map((header) => (
                  <th key={header} className="px-5 py-3 font-semibold">
                    {header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {cost.components.map((component, index) => (
                <tr key={index} className="border-b border-edge/60 last:border-0">
                  <td className="px-5 py-3 font-mono text-xs text-accent">
                    {resourceName(component.resource)}
                  </td>
                  <td className="px-5 py-3 font-mono text-xs text-muted">
                    {component.quantity} {component.unit}
                  </td>
                  <td className="px-5 py-3 font-mono text-xs text-muted">
                    {formatMoney(component.rate)} / hour
                  </td>
                  <td className="px-5 py-3 font-mono text-xs">{formatMoney(component.amount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {cost.topConsumers.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="font-mono text-[11px] uppercase tracking-wider text-muted">
            Where it goes
          </h2>
          <div className="overflow-x-auto rounded-2xl border border-edge bg-surface">
            <table className="w-full border-collapse text-sm">
              <thead>
                <tr className="border-b border-edge text-left font-mono text-[11px] uppercase tracking-wider text-muted">
                  {["Namespace", "Workload", "Run rate this month"].map((header) => (
                    <th key={header} className="px-5 py-3 font-semibold">
                      {header}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {cost.topConsumers.map((workload) => (
                  <tr
                    key={workload.workloadUid}
                    className="border-b border-edge/60 last:border-0 hover:bg-white/[0.03]"
                  >
                    <td className="px-5 py-3 font-mono text-xs text-muted">{workload.namespace}</td>
                    <td className="px-5 py-3 font-medium">{workload.name}</td>
                    <td className="px-5 py-3 font-mono text-xs">{formatMoney(workload.charged)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      ) : null}
    </div>
  );
}

function Tile({
  label,
  amount,
  tone,
}: {
  label: string;
  amount?: Money;
  tone: string;
}) {
  return (
    <div className="flex min-w-[16rem] flex-1 flex-col gap-2 rounded-2xl border border-edge bg-surface px-5 py-4">
      <span className="font-mono text-[11px] uppercase tracking-wider text-muted">{label}</span>
      <span className={`font-mono text-2xl ${tone}`}>{formatMoney(amount)}</span>
    </div>
  );
}

function resourceName(resource: CostComponent_Resource) {
  switch (resource) {
    case CostComponent_Resource.CPU:
      return "cpu";
    case CostComponent_Resource.MEMORY:
      return "memory";
    case CostComponent_Resource.STORAGE:
      return "storage";
    case CostComponent_Resource.LOADBALANCER:
      return "load balancer";
    case CostComponent_Resource.EGRESS:
      return "egress";
    default:
      return "unknown";
  }
}

function formatMoney(amount?: Money) {
  if (!amount) {
    return "—";
  }

  const value = Number(amount.amount);
  if (Number.isNaN(value)) {
    return `${amount.currency} ${amount.amount}`;
  }

  return `${amount.currency} ${value.toLocaleString("id-ID", {
    minimumFractionDigits: 0,
    maximumFractionDigits: 0,
  })}`;
}
