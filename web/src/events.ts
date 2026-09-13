import { useEffect, useState } from "react";
import { fromJson } from "@bufbuild/protobuf";

import type { StreamEvent } from "./gen/marstack/govern/v1/events_pb";
import { StreamEventSchema } from "./gen/marstack/govern/v1/events_pb";

export type StreamState = "connecting" | "live" | "reconnecting" | "resync";

type Handler = (event: StreamEvent) => void;

const handlers = new Map<string, Set<Handler>>();
const stateListeners = new Set<(state: StreamState) => void>();
const resyncListeners = new Set<() => void>();

let source: EventSource | null = null;
let subscribers = 0;
let state: StreamState = "connecting";

const streamedEvents = [
  "workload_changed",
  "division_changed",
  "request_changed",
  "decision_made",
  "projection_degraded",
] as const;

function setState(next: StreamState) {
  state = next;
  for (const listener of stateListeners) {
    listener(next);
  }
}

function open() {
  if (source) {
    return;
  }

  source = new EventSource("/v1/events");
  source.onopen = () => setState("live");
  source.onerror = () => setState("reconnecting");

  source.addEventListener("resync", () => {
    setState("resync");
    for (const listener of resyncListeners) {
      listener();
    }
  });

  for (const name of streamedEvents) {
    source.addEventListener(name, (event) => {
      const decoded = fromJson(
        StreamEventSchema,
        JSON.parse((event as MessageEvent<string>).data),
      );

      setState("live");
      for (const handler of handlers.get(name) ?? []) {
        handler(decoded);
      }
    });
  }
}

function release() {
  subscribers -= 1;
  if (subscribers > 0 || !source) {
    return;
  }

  source.close();
  source = null;
  setState("connecting");
}

function retain() {
  subscribers += 1;
  open();
}

export function useStreamEvent(name: string, handler: Handler) {
  useEffect(() => {
    const set = handlers.get(name) ?? new Set<Handler>();
    set.add(handler);
    handlers.set(name, set);
    retain();

    return () => {
      set.delete(handler);
      release();
    };
  }, [name, handler]);
}

export function useStreamState(): StreamState {
  const [current, setCurrent] = useState(state);

  useEffect(() => {
    stateListeners.add(setCurrent);
    return () => {
      stateListeners.delete(setCurrent);
    };
  }, []);

  return current;
}

export function useResync(handler: () => void) {
  useEffect(() => {
    resyncListeners.add(handler);
    return () => {
      resyncListeners.delete(handler);
    };
  }, [handler]);
}
