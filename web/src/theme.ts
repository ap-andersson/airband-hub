// Auto (follow the system), Dark or Light, remembered per browser. The resolved theme is set
// as data-theme on <html>, which picks the colour tokens in style.css. index.html sets it once
// before the first paint, so the page does not flash in the wrong colours.
import { useEffect, useState } from "preact/hooks";

export type ThemeChoice = "auto" | "dark" | "light";

const KEY = "hub.theme";
const systemLight = matchMedia("(prefers-color-scheme: light)");
const listeners = new Set<() => void>();

function read(): ThemeChoice {
  try {
    const v = localStorage.getItem(KEY);
    return v === "dark" || v === "light" ? v : "auto";
  } catch {
    return "auto";
  }
}

let choice = read();

const resolved = (): "dark" | "light" => (choice === "auto" ? (systemLight.matches ? "light" : "dark") : choice);

function apply() {
  document.documentElement.dataset.theme = resolved();
  listeners.forEach((l) => l());
}

systemLight.addEventListener("change", () => choice === "auto" && apply());
apply();

export function setTheme(c: ThemeChoice) {
  choice = c;
  try {
    localStorage.setItem(KEY, c);
  } catch {
    /* not remembered, still applied */
  }
  apply();
}

/** The choice and the theme it resolves to; re-renders when either changes. */
export function useTheme(): { choice: ThemeChoice; theme: "dark" | "light" } {
  const [, render] = useState(0);
  useEffect(() => {
    const l = () => render((n) => n + 1);
    listeners.add(l);
    return () => void listeners.delete(l);
  }, []);
  return { choice, theme: resolved() };
}
