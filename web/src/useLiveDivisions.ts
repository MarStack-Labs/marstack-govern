import { useCallback, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import { tenancy } from "./client";
import { useResync, useStreamEvent } from "./events";
import type { Freshness } from "./gen/marstack/govern/v1/common_pb";
import type { StreamEvent } from "./gen/marstack/govern/v1/events_pb";
import type { Division, Namespace } from "./gen/marstack/govern/v1/tenancy_pb";

export interface LiveDivisions {
  divisions: Division[];
  namespaces: Namespace[];
  freshness?: Freshness;
  isLoading: boolean;
  error: Error | null;
  refetch: () => void;
}

const divisionsKey = ["divisions"] as const;
const namespacesKey = ["namespaces"] as const;

export function useLiveDivisions(): LiveDivisions {
  const queryClient = useQueryClient();

  const divisionsQuery = useQuery({
    queryKey: divisionsKey,
    queryFn: () => tenancy.listDivisions({}),
  });

  const namespacesQuery = useQuery({
    queryKey: namespacesKey,
    queryFn: () => tenancy.listNamespaces({}),
  });

  const [patches, setPatches] = useState<Map<string, Division>>(new Map());

  const onChange = useCallback(
    (message: StreamEvent) => {
      if (message.body.case !== "divisionChanged") {
        return;
      }

      const division = message.body.value.division;
      if (!division) {
        return;
      }

      setPatches((current) => new Map(current).set(division.uid, division));
      void queryClient.invalidateQueries({ queryKey: namespacesKey });
    },
    [queryClient],
  );

  const onResync = useCallback(() => {
    setPatches(new Map());
    void queryClient.invalidateQueries({ queryKey: divisionsKey });
    void queryClient.invalidateQueries({ queryKey: namespacesKey });
  }, [queryClient]);

  useStreamEvent("division_changed", onChange);
  useResync(onResync);

  const divisions = useMemo(() => {
    const merged = new Map<string, Division>();

    for (const division of divisionsQuery.data?.divisions ?? []) {
      merged.set(division.uid, division);
    }
    for (const [uid, division] of patches) {
      merged.set(uid, division);
    }

    return [...merged.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [divisionsQuery.data, patches]);

  return {
    divisions,
    namespaces: namespacesQuery.data?.namespaces ?? [],
    freshness: divisionsQuery.data?.freshness,
    isLoading: divisionsQuery.isLoading || namespacesQuery.isLoading,
    error: divisionsQuery.error ?? namespacesQuery.error,
    refetch: () => {
      void divisionsQuery.refetch();
      void namespacesQuery.refetch();
    },
  };
}
