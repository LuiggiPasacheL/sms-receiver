"use client";

import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { Check, Smartphone, Trash2, X } from "lucide-react";
import { APIError, request } from "./api";

export type SIM = { id: string; alias: string; messageCount: number };
export const simLabel = (id: string, sims: SIM[]) => sims.find((sim) => sim.id === id)?.alias || id || "Unspecified SIM";

function SIMRow({ sim, busy, onSave, onDelete }: {
  sim: SIM; busy: boolean; onSave: (id: string, alias: string) => Promise<void>; onDelete: (sim: SIM) => void;
}) {
  const inputID = useId();
  const [alias, setAlias] = useState(sim.alias);
  const [saved, setSaved] = useState(false);
  useEffect(() => { setAlias(sim.alias); }, [sim.alias]);
  useEffect(() => {
    if (!saved) return;
    const timer = setTimeout(() => setSaved(false), 2000);
    return () => clearTimeout(timer);
  }, [saved]);
  async function save(event: FormEvent) {
    event.preventDefault();
    setSaved(false);
    try { await onSave(sim.id, alias.trim()); setSaved(true); } catch { /* The manager displays the error. */ }
  }
  return <section className="sim-card" aria-label={`SIM ${sim.id}`}>
    <div className="sim-card-heading"><Smartphone size={18} /><div><h3>{sim.alias || sim.id}</h3><p>{sim.alias && <span>{sim.id} · </span>}{sim.messageCount.toLocaleString()} {sim.messageCount === 1 ? "message" : "messages"}</p></div></div>
    <form className="sim-alias-form" onSubmit={save}>
      <label htmlFor={inputID}>Alias</label>
      <div><input id={inputID} value={alias} maxLength={128} onChange={(event) => { setAlias(event.target.value); setSaved(false); }} placeholder="e.g. Personal or Work" disabled={busy} /><button className="sim-save" disabled={busy || alias.trim() === sim.alias}>{saved ? <><Check size={14} /> Saved</> : "Save alias"}</button></div>
    </form>
    <div className="sim-card-bottom"><span>Leave the alias empty to use the original identifier.</span><button className="sim-delete" disabled={busy} onClick={() => onDelete(sim)}><Trash2 size={14} /> Delete SIM</button></div>
  </section>;
}

export function SIMManager({ sims, onClose, onChanged, onUnauthorized }: {
  sims: SIM[]; onClose: () => void; onChanged: (deletedID?: string) => Promise<void>; onUnauthorized: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<SIM | null>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);

  function report(e: unknown) {
    if (e instanceof APIError && e.status === 401) onUnauthorized();
    else setError(e instanceof Error ? e.message : "Could not update SIM");
  }
  async function saveAlias(id: string, alias: string) {
    setBusy(true);
    setError("");
    try {
      await request("/api/sims", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ sim: id, alias }) });
      await onChanged();
    } catch (e) { report(e); throw e; }
    finally { setBusy(false); }
  }
  async function deleteSIM() {
    if (!deleting) return;
    setBusy(true);
    setError("");
    try {
      await request(`/api/sims?${new URLSearchParams({ sim: deleting.id })}`, { method: "DELETE" });
      setDeleting(null);
      await onChanged(deleting.id);
    } catch (e) { report(e); }
    finally { setBusy(false); }
  }

  const current = deleting ? sims.find((sim) => sim.id === deleting.id) || deleting : null;
  return <dialog className="sim-dialog" ref={dialog} aria-labelledby="sim-manager-title" onClose={onClose} onCancel={(event) => { if (busy) event.preventDefault(); }}>
    <header className="sim-dialog-header"><div><h2 id="sim-manager-title">Manage SIMs</h2><p>Give your SIMs a name or remove their messages.</p></div><button className="icon-button" aria-label="Close SIM manager" disabled={busy} onClick={() => dialog.current?.close()}><X size={18} /></button></header>
    {error && <div className="sim-error" role="alert">{error}</div>}
    {current ? <div className="sim-delete-confirm" role="group" aria-labelledby="sim-delete-title">
      <span className="sim-delete-icon"><Trash2 size={24} /></span><h3 id="sim-delete-title">Delete {current.alias || current.id}?</h3>
      <p>This permanently deletes <strong>all {current.messageCount.toLocaleString()} {current.messageCount === 1 ? "message" : "messages"}</strong> for <strong>{current.id}</strong> from every conversation, along with its alias. This cannot be undone.</p>
      <p>New messages forwarded from this SIM will make it appear again.</p>
      <div className="sim-confirm-actions"><button className="sim-save" autoFocus disabled={busy} onClick={() => { setDeleting(null); setError(""); }}>Cancel</button><button className="sim-delete-confirm-button" disabled={busy} onClick={deleteSIM}>{busy ? "Deleting…" : "Delete SIM and all messages"}</button></div>
    </div> : <div className="sim-cards" aria-busy={busy}>
      {sims.length === 0 ? <div className="sim-empty"><Smartphone size={28} /><h3>No SIMs yet</h3><p>SIMs appear here when messages are forwarded with a SIM identifier.</p></div> : sims.map((sim) => <SIMRow key={sim.id} sim={sim} busy={busy} onSave={saveAlias} onDelete={(value) => { setDeleting(value); setError(""); }} />)}
    </div>}
  </dialog>;
}
