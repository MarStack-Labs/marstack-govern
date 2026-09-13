export type Tone = "healthy" | "progressing" | "degraded" | "neutral" | "accent";

const pills: Record<Tone, string> = {
  healthy: "ms-pill ms-pill-ok",
  progressing: "ms-pill ms-pill-warn",
  degraded: "ms-pill ms-pill-danger",
  accent: "ms-pill ms-pill-info",
  neutral: "ms-pill ms-pill-idle",
};

export function badge(tone: Tone): string {
  return pills[tone];
}

export function dot(): string {
  return "hidden";
}

export const card = "ms-card p-4";

export const cardTitle = "ms-eyebrow";

export const cardSubtitle = "text-sm text-muted";

export const tableWrap = "ms-card overflow-x-auto";

export const tableHead = "";

export const tableRow = "";

export const cell = "";

export const inputField = "ms-input w-full";

export const buttonGhost = "ms-btn";

export const buttonPrimary = "ms-btn ms-btn-primary";
