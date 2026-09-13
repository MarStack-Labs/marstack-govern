import { useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { policies } from "./client";
import { badge, tableHead, tableRow, tableWrap } from "./ui";
import {
  GuardrailPolicy_Enforcement,
  Violation_Result,
  Violation_Severity,
} from "./gen/marstack/govern/v1/policy_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export function PolicyView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";

  const guardrails = useQuery({
    queryKey: ["policies"],
    queryFn: () => policies.listPolicies({}),
    retry: false,
  });

  const violations = useQuery({
    queryKey: ["violations", division],
    queryFn: () => policies.listViolations({ division }),
    enabled: Boolean(division),
    retry: false,
  });

  const compliance = useQuery({
    queryKey: ["compliance", division],
    queryFn: () => policies.getCompliance({ division }),
    enabled: Boolean(division),
    retry: false,
  });

  if (guardrails.error) {
    return (
      <div className="rounded-lg border-l-4 border-progressing bg-progressing-soft px-6 py-6 font-mono text-xs text-progressing">
        {ConnectError.from(guardrails.error).message}
      </div>
    );
  }

  const summary = compliance.data?.compliance;

  return (
    <div className="flex flex-col gap-8">
      {summary ? (
        <section className="flex flex-wrap gap-4">
          <Tile label="resources failing a policy" value={summary.failing} tone="text-degraded" />
          <Tile label="critical findings" value={summary.critical} tone="text-degraded" />
          <Tile label="warnings" value={summary.warning} tone="text-progressing" />
          <Tile label="policies enforced, not just audited" value={summary.policiesEnforced} tone="text-healthy" />
        </section>
      ) : null}

      <section className="flex flex-col gap-3">
        <h2 className="card-title">
          Guardrails in the cluster
        </h2>
        <div className={tableWrap}>
          <table className="ms-table">
            <thead>
              <tr className={tableHead}>
                {["Mode", "Policy", "Scope", "Rules", "What it asks for"].map((header) => (
                  <th key={header} className="font-semibold">
                    {header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(guardrails.data?.policies ?? []).map((guardrail) => (
                <tr
                  key={guardrail.name}
                  className={tableRow}
                >
                  <td >
                    <span
                      className={badge(
                        guardrail.enforcement === GuardrailPolicy_Enforcement.ENFORCE
                          ? "healthy"
                          : "progressing",
                      )}
                    >
                      {guardrail.enforcement === GuardrailPolicy_Enforcement.ENFORCE
                        ? "enforce"
                        : "audit"}
                    </span>
                  </td>
                  <td className="font-medium">{guardrail.name}</td>
                  <td className="font-mono text-xs text-muted">
                    {guardrail.clusterScoped ? "cluster" : "namespace"}
                  </td>
                  <td className="font-mono text-xs text-muted">
                    {guardrail.rules.length}
                  </td>
                  <td className="max-w-[26rem] px-5 py-3 text-muted">
                    {guardrail.description || "—"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="card-title">
          What is failing in {division || "your division"}
        </h2>

        {violations.error ? (
          <p className="font-mono text-xs text-progressing">
            {ConnectError.from(violations.error).message}
          </p>
        ) : (violations.data?.violations ?? []).length === 0 ? (
          <div className="card text-center">
            <p className="text-sm">Nothing is failing a policy right now.</p>
          </div>
        ) : (
          <div className={tableWrap}>
            <table className="ms-table">
              <thead>
                <tr className={tableHead}>
                  {["Severity", "Policy", "Resource", "Namespace", "Message"].map((header) => (
                    <th key={header} className="font-semibold">
                      {header}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(violations.data?.violations ?? []).map((violation, index) => (
                  <tr
                    key={index}
                    className={tableRow}
                  >
                    <td >
                      <span className={severityTone(violation.severity)}>
                        {severityName(violation.severity)}
                        {violation.result === Violation_Result.WARN ? " (warn)" : ""}
                      </span>
                    </td>
                    <td className="font-mono text-xs text-accent">{violation.policy}</td>
                    <td className="font-medium">
                      {violation.resourceKind}/{violation.resourceName}
                    </td>
                    <td className="font-mono text-xs text-muted">{violation.namespace}</td>
                    <td className="max-w-[26rem] px-5 py-3 text-muted">{violation.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
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

function severityName(severity: Violation_Severity) {
  switch (severity) {
    case Violation_Severity.CRITICAL:
      return "critical";
    case Violation_Severity.HIGH:
      return "high";
    case Violation_Severity.LOW:
      return "low";
    default:
      return "medium";
  }
}

function severityTone(severity: Violation_Severity) {
  switch (severity) {
    case Violation_Severity.CRITICAL:
    case Violation_Severity.HIGH:
      return badge("degraded");
    case Violation_Severity.LOW:
      return badge("neutral");
    default:
      return badge("progressing");
  }
}
