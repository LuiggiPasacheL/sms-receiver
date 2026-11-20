"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import { X } from "lucide-react";
import { APIError, request } from "./api";

export function AdminManager({ currentEmail, onClose, onUnauthorized }: {
  currentEmail: string;
  onClose: () => void;
  onUnauthorized: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [emails, setEmails] = useState<string[]>([]);
  const [email, setEmail] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");

  useEffect(() => {
    const controller = new AbortController();
    dialog.current?.showModal();
    request<{ emails: string[] }>("/api/admins", { signal: controller.signal })
      .then((result) => { if (!controller.signal.aborted) setEmails(result.emails); })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (cause instanceof APIError && cause.status === 401) onUnauthorized();
        else setError(cause instanceof Error ? cause.message : "Could not load administrators");
      })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [onUnauthorized]);

  async function add(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    setStatus("");
    try {
      const result = await request<{ email: string }>("/api/admins", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });
      setEmails((previous) => Array.from(new Set([...previous, result.email])).sort());
      setEmail("");
      setStatus(`${result.email} has administrator access.`);
    } catch (cause) {
      if (cause instanceof APIError && cause.status === 401) onUnauthorized();
      else setError(cause instanceof Error ? cause.message : "Could not add administrator");
    } finally {
      setSaving(false);
    }
  }

  return <dialog className="sim-dialog" ref={dialog} aria-labelledby="admin-manager-title" onClose={onClose} onCancel={(event) => { if (saving) event.preventDefault(); }}>
    <header className="sim-dialog-header">
      <div><h2 id="admin-manager-title">Administrators</h2><p>Use an email allowed by your Cloudflare Access policy.</p></div>
      <button className="icon-button" aria-label="Close administrators" disabled={saving} onClick={() => dialog.current?.close()}><X size={18} /></button>
    </header>
    {error && <p className="sim-error" role="alert">{error}</p>}
    <div className="sim-cards">
      <form className="sim-card sim-alias-form" onSubmit={add}>
        <label htmlFor="new-admin-email">Administrator email</label>
        <div>
          <input id="new-admin-email" type="email" autoComplete="off" required maxLength={254} value={email} onChange={(event) => setEmail(event.target.value)} placeholder="name@example.com" disabled={saving} />
          <button className="sim-save" disabled={loading || saving || !email.trim()}>Add administrator</button>
        </div>
        <p role="status" className="admin-status">{status}</p>
      </form>
      {loading ? <p role="status">Loading administrators…</p> : emails.length > 0 &&
        <ul className="admin-email-list" aria-label="Administrator emails">
          {emails.map((address) => <li key={address}>{address}{address === currentEmail && <span> (you)</span>}</li>)}
        </ul>}
    </div>
  </dialog>;
}
