import { useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { decisions, requests } from "./client";
import { useResync, useStreamEvent } from "./events";
import { Decision_Outcome } from "./gen/marstack/govern/v1/decisions_pb";
import type { ResourceRequest } from "./gen/marstack/govern/v1/requests_pb";
import { ResourceRequest_Kind } from "./gen/marstack/govern/v1/requests_pb";

export interface QuotaTarget {
  cpuMillicores: bigint;
  memoryBytes: bigint;
}

export interface SubmitInput {
  division: string;
  target: QuotaTarget;
  reason: string;
}

export interface DecideInput {
  requestUid: string;
  outcome: Decision_Outcome;
  reason: string;
  evidenceDigest: string;
  grantDuration?: string;
}

const requestsKey = ["requests"] as const;
const queueKey = ["queue"] as const;

export function useRequests(division?: string) {
  const queryClient = useQueryClient();

  const list = useQuery({
    queryKey: [...requestsKey, division ?? ""],
    queryFn: () => requests.listRequests({ division: division ?? "" }),
  });

  const queue = useQuery({
    queryKey: [...queueKey, division ?? ""],
    queryFn: () => decisions.listQueue({ division: division ?? "" }),
  });

  const invalidate = useCallback(() => {
    void queryClient.invalidateQueries({ queryKey: requestsKey });
    void queryClient.invalidateQueries({ queryKey: queueKey });
  }, [queryClient]);

  useStreamEvent("request_changed", invalidate);
  useStreamEvent("decision_made", invalidate);
  useResync(invalidate);

  const submit = useMutation({
    mutationFn: (input: SubmitInput) =>
      requests.submitRequest({
        division: input.division,
        kind: ResourceRequest_Kind.QUOTA,
        reason: input.reason,
        quota: { target: input.target },
        idempotencyKey: crypto.randomUUID(),
      }),
    onSuccess: invalidate,
  });

  const decide = useMutation({
    mutationFn: (input: DecideInput) =>
      decisions.decide({
        requestUid: input.requestUid,
        outcome: input.outcome,
        reason: input.reason,
        evidenceDigest: input.evidenceDigest,
        grantDuration: input.grantDuration ?? "",
      }),
    onSuccess: invalidate,
  });

  const withdraw = useMutation({
    mutationFn: (uid: string) => requests.withdrawRequest({ uid, reason: "withdrawn from the console" }),
    onSuccess: invalidate,
  });

  return {
    requests: (list.data?.requests ?? []) as ResourceRequest[],
    queue: (queue.data?.requests ?? []) as ResourceRequest[],
    isLoading: list.isLoading || queue.isLoading,
    error: list.error ?? queue.error,
    refetch: invalidate,
    submit,
    decide,
    withdraw,
  };
}

export function useSimulation(requestUid: string) {
  return useQuery({
    queryKey: ["simulation", requestUid],
    queryFn: () => decisions.simulate({ requestUid }),
    retry: false,
  });
}

export function useRecommendation(division?: string) {
  return useQuery({
    queryKey: ["recommendation", division ?? ""],
    queryFn: () => requests.recommendQuota({ division: division ?? "" }),
    enabled: Boolean(division),
    retry: false,
  });
}
