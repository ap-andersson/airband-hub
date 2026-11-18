import { useEffect, useState } from "preact/hooks";
import type { Live } from "./api";

export type Connection = "connecting" | "open" | "lost";

// Subscribes to the hub's once-a-second state. EventSource reconnects by itself.
export function useLive(): { live: Live | null; connection: Connection } {
  const [live, setLive] = useState<Live | null>(null);
  const [connection, setConnection] = useState<Connection>("connecting");

  useEffect(() => {
    const source = new EventSource("/api/live/stream");
    source.onmessage = (e) => {
      setLive(JSON.parse(e.data) as Live);
      setConnection("open");
    };
    source.onerror = () => setConnection("lost");
    return () => source.close();
  }, []);

  return { live, connection };
}
