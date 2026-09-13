import { useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { finops } from "./client";
import { CostComponent_Resource } from "./gen/marstack/govern/v1/finops_pb";
import type { Money } from "./gen/marstack/govern/v1/common_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";
import { tableHead, tableRow, tableWrap } from "./ui";

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
      <div className="rounded-lg border-l-4 border-degraded bg-degraded-soft px-6 py-6 font-mono text-xs text-degraded">
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
        <h2 className="card-title">Breakdown</h2>
        <div className={tableWrap}>
          <table className="ms-table">
            <thead>
              <tr className={tableHead}>
                {["Resource", "Quantity", "Rate", "Amount"].map((header) => (
                  <th key={header} className="font-semibold">
                    {header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {cost.components.map((component, index) => (
                <tr key={index} className="border-b border-edge/60 last:border-0">
                  <td className="font-mono text-xs text-accent">
                    {resourceName(component.resource)}
                  </td>
                  <td className="font-mono text-xs text-muted">
                    {component.quantity} {component.unit}
                  </td>
                  <td className="font-mono text-xs text-muted">
                    {formatMoney(component.rate)} / hour
                  </td>
                  <td className="font-mono text-xs">{formatMoney(component.amount)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      {cost.topConsumers.length > 0 ? (
        <section className="flex flex-col gap-3">
          <h2 className="card-title">
            Where it goes
          </h2>
          <div className={tableWrap}>
            <table className="ms-table">
              <thead>
                <tr className={tableHead}>
                  {["Namespace", "Workload", "Run rate this month"].map((header) => (
                    <th key={header} className="font-semibold">
                      {header}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {cost.topConsumers.map((workload) => (
                  <tr
                    key={workload.workloadUid}
                    className={tableRow}
                  >
                    <td className="font-mono text-xs text-muted">{workload.namespace}</td>
                    <td className="font-medium">{workload.name}</td>
                    <td className="font-mono text-xs">{formatMoney(workload.charged)}</td>
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
    <div className="flex min-w-0 sm:min-w-[16rem] flex-1 flex-col gap-2 card px-5 py-4">
      <span className="card-title">{label}</span>
      <span className={`text-2xl font-medium ${tone}`}>{formatMoney(amount)}</span>
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
