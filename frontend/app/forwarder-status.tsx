"use client";

import { useEffect, useState } from "react";
import { Smartphone } from "lucide-react";
import { APIError, request } from "./api";

type Status = {
  status: "disabled" | "unknown" | "online" | "offline";
  lastSeen: string | null;
  serverTime: string;
  timeoutSeconds: number;
};
type Snapshot = { value: Status; expiresAt: number };

export function ForwarderStatus({ revision, connected, onUnauthorized }: {
  revision: number;
  connected: boolean;
  onUnauthorized: () => void;
}) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [failed, setFailed] = useState(false);
  const [now, setNow] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    const started = performance.now();
    request<Status>("/api/forwarder", { signal: controller.signal }).then((value) => {
      if (controller.signal.aborted) return;
      // Use the server's clock and elapsed monotonic time, even if the phone or
      // browser clock is wrong. Count request latency conservatively.
      const remaining = value.lastSeen
        ? Date.parse(value.lastSeen) + value.timeoutSeconds * 1000 - Date.parse(value.serverTime)
        : 0;
      setSnapshot({ value, expiresAt: started + remaining });
      setNow(performance.now());
      setFailed(false);
    }).catch((error: unknown) => {
      if (controller.signal.aborted) return;
      setFailed(true);
      if (error instanceof APIError && error.status === 401) onUnauthorized();
    });
    return () => controller.abort();
  }, [revision, connected, onUnauthorized]);

  useEffect(() => {
    if (!snapshot || snapshot.value.status !== "online") return;
    const remaining = snapshot.expiresAt - performance.now();
    // Browsers round timer delays; an early callback must still expire this
    // snapshot instead of leaving it online until the next server event.
    const timer = setTimeout(() => setNow(Math.max(performance.now(), snapshot.expiresAt)), Math.max(0, Math.ceil(remaining)));
    return () => clearTimeout(timer);
  }, [snapshot]);

  const value = snapshot?.value;
  let status: Status["status"] | "unavailable" | "loading" = value?.status || "loading";
  if (failed || !connected) status = "unavailable";
  else if (status === "online" && snapshot && now >= snapshot.expiresAt) status = "offline";
  const labels = {
    loading: "Checking phone…",
    disabled: "Phone monitoring disabled",
    unknown: "Waiting for first heartbeat",
    online: "Phone active",
    offline: "Phone inactive",
    unavailable: "Phone status unavailable",
  };
  const lastSeen = value?.lastSeen ? new Date(value.lastSeen).toLocaleString() : null;
  const detail = status === "disabled" ? "Heartbeat monitoring is not enabled for this inbox."
    : status === "unknown" ? "Enable heartbeat in SMS Forwarder settings."
    : status === "unavailable" ? "Waiting for the inbox connection to recover."
    : status === "offline" ? "No heartbeat within the expected time."
    : "SMS Forwarder heartbeat";

  return <div className={`forwarder-status forwarder-${status}`} role="status" title={`${detail}${lastSeen ? ` Last heartbeat: ${lastSeen}.` : ""}`}>
    <Smartphone size={15} aria-hidden="true" />
    <span className={`status-dot ${status === "online" ? "" : "offline"}`} />
    <strong>{labels[status]}</strong>
    <span className="forwarder-last-seen">{lastSeen ? `Last heartbeat: ${lastSeen}` : detail}</span>
  </div>;
}
