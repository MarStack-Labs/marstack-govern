import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { catalog } from "./client";
import { useResync, useStreamEvent } from "./events";
import type { Workload } from "./gen/marstack/govern/v1/catalog_pb";
import type { Freshness } from "./gen/marstack/govern/v1/common_pb";
import type { StreamEvent } from "./gen/marstack/govern/v1/events_pb";
import { WorkloadChanged_Change } from "./gen/marstack/govern/v1/events_pb";

export interface LiveWorkloads {
  workloads: Workload[];
  freshness?: Freshness;
  isLoading: boolean;
  error: Error | null;
  refetch: () => void;
}

const workloadsKey = ["workloads"] as const;

export function useLiveWorkloads(): LiveWorkloads {
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: workloadsKey,
    queryFn: () => catalog.listWorkloads({ page: { size: 200 } }),
  });

  const [patches, setPatches] = useState<Map<string, Workload | null>>(new Map());
  const [liveFreshness, setLiveFreshness] = useState<Freshness | undefined>();

  const onChange = useCallback((message: StreamEvent) => {
    if (message.body.case !== "workloadChanged") {
      return;
    }

    const { change, workload } = message.body.value;
    if (!workload) {
      return;
    }

    setLiveFreshness(message.freshness);
    setPatches((current) => {
      const next = new Map(current);
      next.set(
        workload.uid,
        change === WorkloadChanged_Change.REMOVED ? null : workload,
      );
      return next;
    });
  }, []);

  const onResync = useCallback(() => {
    setPatches(new Map());
    void queryClient.invalidateQueries({ queryKey: workloadsKey });
  }, [queryClient]);

  useStreamEvent("workload_changed", onChange);
  useResync(onResync);

  const workloads = useMemo(() => {
    const merged = new Map<string, Workload>();

    for (const workload of query.data?.workloads ?? []) {
      merged.set(workload.uid, workload);
    }

    for (const [uid, workload] of patches) {
      if (workload === null) {
        merged.delete(uid);
      } else {
        merged.set(uid, workload);
      }
    }

    return [...merged.values()].sort((a, b) => {
      const byNamespace = a.namespace.localeCompare(b.namespace);
      return byNamespace !== 0 ? byNamespace : a.name.localeCompare(b.name);
    });
  }, [query.data, patches]);

  return {
    workloads,
    freshness: liveFreshness ?? query.data?.freshness,
    isLoading: query.isLoading,
    error: query.error,
    refetch: () => void query.refetch(),
  };
}
