// Changes need the admin passphrase. A change answered with 401 opens this dialog; once logged
// in, the change is retried. The login is a cookie the page cannot read, so the page keeps its
// own note of whether it is logged in.
import { useEffect, useRef, useState } from "preact/hooks";

type Waiter = { resolve: () => void; reject: (e: Error) => void };

let open: ((w: Waiter) => void) | null = null;
let loggedIn: boolean | null = null; // null: not known yet; the hub's live state says
const listeners = new Set<() => void>();

function setLoggedIn(v: boolean) {
  loggedIn = v;
  listeners.forEach((l) => l());
}

/** Asks for the passphrase; resolves once logged in, rejects if the dialog is cancelled. */
export function requestLogin(): Promise<void> {
  return new Promise((resolve, reject) => (open ? open({ resolve, reject }) : reject(new Error("Log in to do this"))));
}

export async function logout() {
  await fetch("/api/logout", { method: "POST" });
  setLoggedIn(false);
}

/** Whether this browser is logged in; fromHub is the hub's answer, used until the page knows better. */
export function useLoggedIn(fromHub: boolean): boolean {
  const [, render] = useState(0);
  useEffect(() => {
    const l = () => render((n) => n + 1);
    listeners.add(l);
    return () => void listeners.delete(l);
  }, []);
  return loggedIn ?? fromHub;
}

export function LoginDialog() {
  const dialog = useRef<HTMLDialogElement>(null);
  const waiter = useRef<Waiter | null>(null);
  const [phrase, setPhrase] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    open = (w) => {
      waiter.current?.reject(new Error("Log in to do this"));
      waiter.current = w;
      setPhrase("");
      setError(null);
      dialog.current?.showModal();
    };
    return () => void (open = null);
  }, []);

  const close = () => {
    dialog.current?.close();
    waiter.current?.reject(new Error("Log in to do this"));
    waiter.current = null;
  };

  return (
    <dialog ref={dialog} class="login" onCancel={(e) => (e.preventDefault(), close())} aria-labelledby="login-title">
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const r = await fetch("/api/login", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ passphrase: phrase }),
          }).catch(() => null);
          setBusy(false);
          if (r?.ok) {
            setLoggedIn(true);
            dialog.current?.close();
            waiter.current?.resolve();
            waiter.current = null;
          } else {
            setError(r ? (await r.text()).trim() || r.statusText : "Cannot reach the hub");
          }
        }}
      >
        <h2 id="login-title">Log in to make changes</h2>
        <label>
          Passphrase
          <input type="password" autocomplete="current-password" value={phrase} onInput={(e) => setPhrase(e.currentTarget.value)} autofocus />
        </label>
        {error && <p class="form-error">{error}</p>}
        <div class="login-buttons">
          <button type="submit" class="primary" disabled={!phrase || busy}>
            Log in
          </button>
          <button type="button" onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </dialog>
  );
}
