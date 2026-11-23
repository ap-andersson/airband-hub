import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";

const EVENT = "hub-navigate";

// replace: a change within the current view (e.g. stepping through days); the scroll position
// stays. Otherwise it is a new page, which starts at the top, as a link would.
export function navigate(to: string, replace = false) {
  if (replace) history.replaceState(null, "", to);
  else {
    history.pushState(null, "", to);
    scrollTo(0, 0);
  }
  dispatchEvent(new Event(EVENT));
}

// The current URL, re-rendered on navigation and on back/forward.
export function useLocation(): URL {
  const [href, setHref] = useState(location.href);
  useEffect(() => {
    const update = () => setHref(location.href);
    addEventListener("popstate", update);
    addEventListener(EVENT, update);
    return () => {
      removeEventListener("popstate", update);
      removeEventListener(EVENT, update);
    };
  }, []);
  return new URL(href);
}

export function Link({ href, class: cls, children, current }: { href: string; class?: string; children: ComponentChildren; current?: boolean }) {
  return (
    <a
      href={href}
      class={cls}
      aria-current={current ? "page" : undefined}
      onClick={(e) => {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        navigate(href);
      }}
    >
      {children}
    </a>
  );
}
