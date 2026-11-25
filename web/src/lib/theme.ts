// Light/dark following the system, with a stored override.
export type Theme = "light" | "dark";
const KEY = "atlantis-theme";

export function initialTheme(): Theme {
  try {
    const t = localStorage.getItem(KEY);
    if (t === "light" || t === "dark") return t;
  } catch {
    /* storage unavailable */
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function applyTheme(t: Theme, persist = false) {
  document.documentElement.dataset.theme = t;
  if (persist) {
    try {
      localStorage.setItem(KEY, t);
    } catch {
      /* storage unavailable */
    }
  }
}
