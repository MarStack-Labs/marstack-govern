import { useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import { delivery } from "./client";
import type { Template } from "./gen/marstack/govern/v1/delivery_pb";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export function DeployView({ session }: { session?: Session }) {
  const division = session?.activeDivision ?? "";
  const [selected, setSelected] = useState<string>("");
  const [name, setName] = useState("");
  const [environment, setEnvironment] = useState("dev");
  const [values, setValues] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");

  const templates = useQuery({
    queryKey: ["templates"],
    queryFn: () => delivery.listTemplates({}),
    retry: false,
  });

  const preview = useMutation({
    mutationFn: () =>
      delivery.scaffold({ division, environment, template: selected, name, values }),
  });

  const open = useMutation({
    mutationFn: () =>
      delivery.openChange({ division, environment, template: selected, name, values, reason }),
  });

  if (templates.error) {
    return (
      <div className="rounded-lg border-l-4 border-progressing bg-progressing-soft px-6 py-6 font-mono text-xs text-progressing">
        {ConnectError.from(templates.error).message}
      </div>
    );
  }

  const catalogue = templates.data?.templates ?? [];
  const template = catalogue.find((candidate) => candidate.name === selected);

  return (
    <div className="flex flex-col gap-8">
      <section className="flex flex-col gap-3">
        <h2 className="card-title">
          Curated templates, read from the registry
        </h2>

        {catalogue.length === 0 ? (
          <div className="card text-center">
            <p className="text-sm">The registry holds no charts yet.</p>
          </div>
        ) : (
          <div className="flex flex-wrap gap-3">
            {catalogue.map((candidate) => (
              <button
                key={candidate.name}
                type="button"
                onClick={() => {
                  setSelected(candidate.name);
                  setValues(defaultsOf(candidate));
                  preview.reset();
                  open.reset();
                }}
                className={`flex min-w-0 sm:min-w-[16rem] flex-1 flex-col gap-1 rounded-lg border px-5 py-4 text-left transition-colors ${
                  candidate.name === selected
                    ? "border-accent bg-primary-soft"
                    : "border-edge bg-surface hover:bg-primary-soft/60"
                }`}
              >
                <span className="font-medium">{candidate.name}</span>
                <span className="font-mono text-[11px] text-muted">{candidate.version}</span>
                <span className="text-sm text-muted">{candidate.description || "—"}</span>
              </button>
            ))}
          </div>
        )}
      </section>

      {template ? (
        <section className="flex flex-col gap-4 card px-6 py-5">
          <h2 className="card-title">
            Add {template.name} to {division || "your division"}
          </h2>

          <div className="flex flex-wrap gap-4">
            <Field label="Name">
              <input
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="ledger-db"
                className="w-full rounded-lg border border-edge bg-raised px-3 py-1.5 font-mono text-xs text-ink"
              />
            </Field>
            <Field label="Environment">
              <input
                value={environment}
                onChange={(event) => setEnvironment(event.target.value)}
                className="w-full rounded-lg border border-edge bg-raised px-3 py-1.5 font-mono text-xs text-ink"
              />
            </Field>
          </div>

          <div className="flex flex-wrap gap-4">
            {template.fields.map((field) => (
              <Field key={field.name} label={`${field.label}${field.required ? " *" : ""}`}>
                <input
                  value={values[field.name] ?? ""}
                  onChange={(event) =>
                    setValues((current) => ({ ...current, [field.name]: event.target.value }))
                  }
                  placeholder={field.defaultValue || field.kind}
                  className="w-full rounded-lg border border-edge bg-raised px-3 py-1.5 font-mono text-xs text-ink"
                />
              </Field>
            ))}
          </div>

          <div className="flex flex-wrap items-end gap-3">
            <Field label="Why this is being added">
              <input
                value={reason}
                onChange={(event) => setReason(event.target.value)}
                placeholder="recorded on the merge request"
                className="w-full rounded-lg border border-edge bg-raised px-3 py-1.5 font-mono text-xs text-ink"
              />
            </Field>

            <button
              type="button"
              disabled={!name || preview.isPending}
              onClick={() => preview.mutate()}
              className="rounded-lg border border-edge px-4 py-1.5 font-mono text-[11px] text-muted hover:text-ink disabled:opacity-40"
            >
              preview
            </button>
            <button
              type="button"
              disabled={!name || reason.trim().length < 10 || open.isPending}
              onClick={() => open.mutate()}
              className="rounded-lg border border-accent px-4 py-1.5 font-mono text-[11px] text-accent hover:bg-primary-soft disabled:opacity-40"
            >
              open merge request
            </button>
          </div>

          {preview.error ? (
            <p className="font-mono text-xs text-degraded">
              {ConnectError.from(preview.error).message}
            </p>
          ) : null}
          {open.error ? (
            <p className="font-mono text-xs text-degraded">
              {ConnectError.from(open.error).message}
            </p>
          ) : null}

          {open.data?.change ? (
            <p className="font-mono text-xs text-healthy">
              opened{" "}
              <a href={open.data.change.url} className="underline" target="_blank" rel="noreferrer">
                {open.data.change.branch}
              </a>
            </p>
          ) : null}

          {preview.data ? (
            <div className="flex flex-col gap-3">
              <p
                className={`font-mono text-xs ${
                  preview.data.admitted ? "text-healthy" : "text-progressing"
                }`}
              >
                {preview.data.admitted
                  ? "the cluster would accept this"
                  : "the cluster would not accept this as written"}
              </p>

              {preview.data.findings.map((finding, index) => (
                <p key={index} className="font-mono text-xs text-muted">
                  {finding.check}: {finding.message}
                </p>
              ))}

              {preview.data.files.map((file) => (
                <div key={file.path} className="flex flex-col gap-1">
                  <span className="font-mono text-[11px] text-accent">{file.path}</span>
                  <pre className="overflow-x-auto rounded-lg border border-edge bg-raised px-4 py-3 font-mono text-[11px] text-muted">
                    {file.content}
                  </pre>
                </div>
              ))}
            </div>
          ) : null}
        </section>
      ) : null}
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex min-w-0 sm:min-w-[14rem] flex-1 flex-col gap-1.5">
      <span className="card-title">{label}</span>
      {children}
    </label>
  );
}

function defaultsOf(template: Template): Record<string, string> {
  const values: Record<string, string> = {};
  for (const field of template.fields) {
    values[field.name] = field.defaultValue;
  }

  return values;
}
