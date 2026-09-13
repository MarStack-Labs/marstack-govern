import { useEffect } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";

import { sessions } from "./client";
import type { Session } from "./gen/marstack/govern/v1/identity_pb";

export interface SessionState {
  session?: Session;
  isLoading: boolean;
  switchDivision: (division: string) => void;
  signOut: () => void;
}

const sessionKey = ["session"] as const;

export function useSession(): SessionState {
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: sessionKey,
    queryFn: () => sessions.getSession({}),
    retry: false,
    staleTime: 60_000,
  });

  useEffect(() => {
    if (query.error && ConnectError.from(query.error).code === Code.Unauthenticated) {
      const here = window.location.pathname + window.location.search;
      window.location.href = `/auth/login?redirect=${encodeURIComponent(here)}`;
    }
  }, [query.error]);

  const switching = useMutation({
    mutationFn: (division: string) => sessions.switchDivision({ division }),
    onSuccess: () => {
      void queryClient.invalidateQueries();
    },
  });

  return {
    session: query.data?.session,
    isLoading: query.isLoading,
    switchDivision: (division: string) => switching.mutate(division),
    signOut: () => {
      void fetch("/auth/logout", { method: "POST" }).then(() => {
        window.location.href = "/";
      });
    },
  };
}
