"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { ArrowDown, ArrowLeft, ArrowUpRight, Check, CheckCheck, ChevronDown, Copy, Inbox, LockKeyhole, LogOut, MessageSquare, Radio, RefreshCw, Search, ShieldCheck, Signal, Smartphone, Settings2, Users, X } from "lucide-react";

import { APIError, request } from "./api";
import { AdminManager } from "./admin-manager";
import { SIMManager, simLabel, type SIM } from "./sim-manager";
import { findVerificationCode } from "./verification-code";

type Message = {
  id: string;
  from: string;
  text: string;
  sentStamp: string | number | null;
  receivedStamp: string | number | null;
  sim: string;
  createdAt: string;
};
type Conversation = { latestMessage: Message; messageCount: number };
type ConversationPage = { conversations: Conversation[]; nextCursor?: string; total: number; messageTotal: number; today: number; sims: string[] };
type Page = { messages: Message[]; nextCursor?: string; total: number; today: number; sims: string[] };
type Session = { authenticated: boolean; passwordRequired?: boolean; provider?: "cloudflare"; email?: string; admin?: boolean };
type Connection = "connecting" | "live" | "reconnecting";

const number = (n: number) => new Intl.NumberFormat().format(n);
function dateLabel(value: string, full = false) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, full
    ? { dateStyle: "medium", timeStyle: "medium" }
    : { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(date);
}
function stampLabel(value: Message["sentStamp"]) {
  if (value === null || value === "") return "Not provided";
  const numeric = typeof value === "number" || /^\d+(\.\d+)?$/.test(value) ? Number(value) : NaN;
  if (Number.isFinite(numeric)) {
    const date = new Date(numeric < 100_000_000_000 ? numeric * 1000 : numeric);
    return Number.isNaN(date.getTime()) ? String(value) : dateLabel(date.toISOString(), true);
  }
  // Only parse strings with an explicit timezone; local phone formats stay intact.
  if (/^\d{4}-\d{2}-\d{2}T.*(?:Z|[+-]\d{2}:?\d{2})$/.test(String(value))) return dateLabel(String(value), true);
  return String(value);
}
function initials(sender: string) {
  if (/^[+\d\s()-]+$/.test(sender)) return <Smartphone size={20} strokeWidth={1.6} />;
  return sender.replace(/[^a-zA-Z0-9]/g, "").slice(0, 2).toUpperCase() || <MessageSquare size={19} />;
}
function Brand() {
  return <div className="brand"><span className="brand-mark"><MessageSquare size={22} strokeWidth={1.7} /></span><span>relay<span className="brand-dot">.</span></span></div>;
}

function copyWithSelection(text: string) {
  // Clipboard.writeText is unavailable on HTTP, including LAN deployments.
  const focused = document.activeElement;
  const selection = document.getSelection();
  const ranges = selection ? Array.from({ length: selection.rangeCount }, (_, index) => selection.getRangeAt(index)) : [];
  const field = document.createElement("textarea");
  field.value = text;
  field.readOnly = true;
  field.style.cssText = "position:fixed;top:0;left:0;opacity:0;pointer-events:none;font-size:16px";
  document.body.append(field);
  try {
    field.focus({ preventScroll: true });
    field.select();
    if (!document.execCommand("copy")) throw new Error("Clipboard unavailable");
  } finally {
    field.remove();
    if (focused instanceof HTMLElement) focused.focus({ preventScroll: true });
    if (selection) {
      selection.removeAllRanges();
      for (const range of ranges) selection.addRange(range);
    }
  }
}

function ConversationView({ sender, sim, sims, revision, isOpen, onBack, onUnauthorized }: {
  sender: string; sim: string; sims: SIM[]; revision: number; isOpen: boolean; onBack: () => void; onUnauthorized: () => void;
}) {
  const [messages, setMessages] = useState<Message[]>([]);
  const [pageCount, setPageCount] = useState(1);
  const [retry, setRetry] = useState(0);
  const [total, setTotal] = useState(0);
  const [hasNext, setHasNext] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState<{ id: string; kind: "message" | "code" } | null>(null);
  const scroll = useRef<HTMLDivElement>(null);
  const loadedPages = useRef(0);
  const position = useRef({ top: 0, height: 0, stick: true, prepend: false });

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError("");
    (async () => {
      try {
        const all: Message[] = [];
        let cursor = "";
        let result: Page | null = null;
        for (let index = 0; index < pageCount; index++) {
          const params = new URLSearchParams({ from: sender, sim, limit: "50" });
          if (cursor) params.set("before", cursor);
          result = await request<Page>(`/api/messages?${params}`, { signal: controller.signal });
          all.push(...result.messages);
          cursor = result.nextCursor || "";
          if (!cursor) break;
        }
        if (controller.signal.aborted || !result) return;
        const pane = scroll.current;
        position.current = {
          top: pane?.scrollTop || 0,
          height: pane?.scrollHeight || 0,
          stick: loadedPages.current === 0 || Boolean(pane && pane.scrollHeight - pane.scrollTop - pane.clientHeight < 80),
          prepend: loadedPages.current > 0 && pageCount > loadedPages.current,
        };
        loadedPages.current = pageCount;
        setMessages(all.reverse());
        setTotal(result.total);
        setHasNext(Boolean(cursor));
      } catch (e) {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) onUnauthorized();
        else setError(e instanceof Error ? e.message : "Could not load this conversation");
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [sender, sim, revision, pageCount, retry, onUnauthorized]);

  useLayoutEffect(() => {
    const pane = scroll.current;
    if (!pane) return;
    const previous = position.current;
    if (previous.prepend) pane.scrollTop = previous.top + pane.scrollHeight - previous.height;
    else if (previous.stick) pane.scrollTop = pane.scrollHeight;
  }, [messages]);

  useLayoutEffect(() => {
    // On phones the selected conversation can load while its pane is hidden.
    // Scroll after opening it, once the pane has a measurable height.
    if (isOpen && scroll.current) scroll.current.scrollTop = scroll.current.scrollHeight;
  }, [isOpen]);

  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(null), 2000);
    return () => clearTimeout(timer);
  }, [copied]);

  async function copyText(id: string, kind: "message" | "code", text: string) {
    try {
      if (navigator.clipboard?.writeText) {
        try { await navigator.clipboard.writeText(text); }
        catch { copyWithSelection(text); }
      } else {
        copyWithSelection(text);
      }
      setCopied({ id, kind });
    } catch {
      setCopied(null);
      setError("Copy is unavailable in this browser. You can select and copy the text in the message.");
    }
  }

  return <>
    <header className="conversation-header">
      <div className="detail-topline"><button className="back-button" onClick={onBack}><ArrowLeft size={16} /> Inbox</button><span className="eyebrow">CONVERSATION</span><span className="received-chip"><LockKeyhole size={11} /> Read-only</span></div>
      <div className="conversation-identity"><span className={`avatar avatar-${sender.charCodeAt(0) % 4}`}>{initials(sender)}</span><div><h2 tabIndex={-1}>{sender}</h2><p>{loading && total === 0 ? "Loading messages…" : `${number(total)} ${total === 1 ? "message" : "messages"}`}{sim && ` · ${simLabel(sim, sims)}`}</p></div></div>
    </header>
    {error && <div className="error-banner" role="alert"><span>{error}</span><button onClick={() => setRetry((r) => r + 1)}>Retry</button></div>}
    <div className="conversation-messages" ref={scroll} aria-busy={loading} aria-label={`Messages from ${sender}`}>
      {hasNext && <button className="load-more" disabled={loading} onClick={() => setPageCount((p) => p + 1)}>{loading ? "Loading…" : "Load older messages"}<ArrowDown size={13} /></button>}
      {messages.length === 0 && <div className="list-empty" role="status">{loading ? "Loading conversation…" : error ? "Conversation unavailable. Try again." : "No messages in this conversation."}</div>}
      {messages.map((message, index) => {
        const day = new Date(message.createdAt).toLocaleDateString(undefined, { dateStyle: "long" });
        const previousDay = index > 0 ? new Date(messages[index - 1].createdAt).toLocaleDateString(undefined, { dateStyle: "long" }) : "";
        const code = findVerificationCode(message.text);
        const copiedCode = copied?.id === message.id && copied.kind === "code";
        const copiedMessage = copied?.id === message.id && copied.kind === "message";
        return <div className="chat-entry" key={message.id}>
          {day !== previousDay && <div className="chat-date"><span>{day}</span></div>}
          <div className="chat-bubble">
            <p className="message-body" dir="auto">{message.text}</p>
            {code && <button type="button" className="verification-code-button" onClick={() => copyText(message.id, "code", code)}>
              {copiedCode ? <Check size={15} /> : <Copy size={15} />}
              <span aria-live="polite">{copiedCode ? "Code copied" : "Copy code"}</span>
            </button>}
            <div className="chat-bubble-footer"><span className="sim-badge" title={message.sim}><Smartphone size={10} />{simLabel(message.sim, sims)}</span><time dateTime={message.createdAt} title={dateLabel(message.createdAt, true)}>{new Date(message.createdAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time><button className="copy-button" onClick={() => copyText(message.id, "message", message.text)} aria-label={`Copy message ${message.id}`}>{copiedMessage ? <Check size={14} /> : <Copy size={14} />}{copiedMessage ? "Copied" : "Copy"}</button></div>
            <details className="chat-metadata"><summary>Message details</summary><dl className="metadata"><div><dt>Sent at</dt><dd>{stampLabel(message.sentStamp)}</dd></div><div><dt>Received on phone</dt><dd>{stampLabel(message.receivedStamp)}</dd></div><div><dt>Forwarded to inbox</dt><dd>{dateLabel(message.createdAt, true)}</dd></div><div><dt>Message ID</dt><dd>#{message.id}</dd></div></dl></details>
          </div>
        </div>;
      })}
    </div>
    <div className="conversation-note"><LockKeyhole size={13} /><p>Read-only conversation. Replies are handled on your phone.</p></div>
  </>;
}

export default function Home() {
  const [session, setSession] = useState<Session | null>(null);
  const [sessionError, setSessionError] = useState("");
  const [password, setPassword] = useState("");
  const [signingIn, setSigningIn] = useState(false);
  const [search, setSearch] = useState("");
  const [term, setTerm] = useState("");
  const [sim, setSim] = useState("");
  const [sims, setSIMs] = useState<SIM[]>([]);
  const [managingSIMs, setManagingSIMs] = useState(false);
  const [managingAdmins, setManagingAdmins] = useState(false);
  const [pageCount, setPageCount] = useState(1);
  const [revision, setRevision] = useState(0);
  const [conversations, setConversations] = useState<Conversation[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [mobileDetail, setMobileDetail] = useState(false);
  const [stats, setStats] = useState({ total: 0, conversations: 0, today: 0, sims: [] as string[] });
  const [hasNext, setHasNext] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [connection, setConnection] = useState<Connection>("connecting");
  const [lastSync, setLastSync] = useState<Date | null>(null);
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const messageList = useRef<HTMLDivElement>(null);
  const messageDetail = useRef<HTMLElement>(null);
  const openedRow = useRef<HTMLButtonElement | null>(null);
  const listScrollTop = useRef(0);

  useEffect(() => {
    if (mobileDetail) {
      if (window.matchMedia("(max-width: 700px), (max-height: 520px) and (max-width: 960px)").matches) {
        messageDetail.current?.scrollTo({ top: 0 });
        messageDetail.current?.querySelector<HTMLHeadingElement>("h2")?.focus({ preventScroll: true });
      }
    } else if (openedRow.current?.isConnected) {
      if (messageList.current) messageList.current.scrollTop = listScrollTop.current;
      openedRow.current.focus({ preventScroll: true });
    }
  }, [mobileDetail]);

  function checkSession() {
    setSessionError("");
    request<Session>("/api/session").then(setSession).catch((e: Error) => setSessionError(e.message));
  }
  useEffect(() => { checkSession(); }, []);
  useEffect(() => {
    const timer = setTimeout(() => {
      setTerm(search.trim());
      setPageCount(1);
    }, 250);
    return () => clearTimeout(timer);
  }, [search]);

  const authenticated = session?.authenticated;
  const sessionExpired = useCallback(() => {
    setSession((current) => current ? { ...current, authenticated: false, email: undefined, admin: false } : current);
    setManagingAdmins(false);
    setManagingSIMs(false);
  }, []);
  useEffect(() => {
    if (!authenticated) return;
    setConversations([]);
    setSelected(null);
    openedRow.current = null;
    listScrollTop.current = 0;
    setMobileDetail(false);
  }, [authenticated, term, sim]);

  useEffect(() => {
    if (!authenticated) return;
    const controller = new AbortController();
    setLoading(true);
    setError("");
    (async () => {
      try {
        let cursor = "";
        let result: ConversationPage | null = null;
        const all: Conversation[] = [];
        for (let index = 0; index < pageCount; index++) {
          const params = new URLSearchParams({ q: term, sim, limit: "50" });
          if (cursor) params.set("before", cursor);
          result = await request<ConversationPage>(`/api/conversations?${params}`, { signal: controller.signal });
          all.push(...result.conversations);
          cursor = result.nextCursor || "";
          if (!cursor) break;
        }
        if (controller.signal.aborted || !result) return;
        setConversations(all);
        setHasNext(Boolean(cursor));
        setStats({ total: result.messageTotal, conversations: result.total, today: result.today, sims: result.sims });
        setSelected((current) => current && (cursor || all.some((c) => c.latestMessage.from === current)) ? current : all[0]?.latestMessage.from || null);
        setLastSync(new Date());
      } catch (e) {
        if (controller.signal.aborted) return;
        if (e instanceof APIError && e.status === 401) sessionExpired();
        else setError(e instanceof Error ? e.message : "Could not load messages");
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    })();
    return () => controller.abort();
  }, [authenticated, term, sim, pageCount, revision, sessionExpired]);

  useEffect(() => {
    if (!authenticated) return;
    setConnection("connecting");
    const events = new EventSource("/api/events");
    const refresh = () => {
      if (refreshTimer.current) return;
      refreshTimer.current = setTimeout(() => {
        refreshTimer.current = null;
        setRevision((r) => r + 1);
      }, 180);
    };
    events.addEventListener("ready", () => { setConnection("live"); refresh(); });
    events.addEventListener("messages", refresh);
    events.onerror = () => {
      setConnection("reconnecting");
      request<Session>("/api/session").then(setSession).catch(() => {});
    };
    const onVisible = () => { if (document.visibilityState === "visible") refresh(); };
    document.addEventListener("visibilitychange", onVisible);
    window.addEventListener("online", refresh);
    return () => {
      events.close();
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener("online", refresh);
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
      refreshTimer.current = null;
    };
  }, [authenticated]);

  const loadSIMs = useCallback(async (signal?: AbortSignal) => {
    const result = await request<{ sims: SIM[] }>("/api/sims", { signal });
    if (signal?.aborted) return;
    setSIMs(result.sims);
    setSim((current) => current && !result.sims.some((value) => value.id === current) ? "" : current);
  }, []);

  useEffect(() => {
    if (!authenticated) return;
    const controller = new AbortController();
    loadSIMs(controller.signal).catch((e: unknown) => {
      if (controller.signal.aborted) return;
      if (e instanceof APIError && e.status === 401) sessionExpired();
      else setError(e instanceof Error ? e.message : "Could not load SIMs");
    });
    return () => controller.abort();
  }, [authenticated, revision, loadSIMs, sessionExpired]);

  async function simsChanged(deletedID?: string) {
    // Refresh the inbox even if the metadata request fails after a mutation.
    setRevision((r) => r + 1);
    if (deletedID !== undefined) {
      setSIMs((current) => current.filter((value) => value.id !== deletedID));
      setSim((current) => current === deletedID ? "" : current);
      setPageCount(1);
      setSelected(null);
      setMobileDetail(false);
    }
    await loadSIMs();
  }

  async function login(event: FormEvent) {
    event.preventDefault();
    setSigningIn(true);
    setSessionError("");
    try {
      await request("/api/session", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password }) });
      setSession({ authenticated: true, passwordRequired: true });
      setPassword("");
    } catch (e) { setSessionError(e instanceof Error ? e.message : "Sign in failed"); }
    finally { setSigningIn(false); }
  }
  async function logout() {
    try {
      await request("/api/session", { method: "DELETE" });
      setSession({ authenticated: false, passwordRequired: true });
      setConversations([]);
      setSelected(null);
      setStats({ total: 0, conversations: 0, today: 0, sims: [] });
      setLastSync(null);
      setSIMs([]);
      setManagingSIMs(false);
    } catch (e) { setError(e instanceof Error ? e.message : "Could not sign out"); }
  }
  if (!session || !session.authenticated) return (
    <main className="auth-shell">
      <div className="auth-brand"><Brand /><span className="eyebrow">YOUR PERSONAL SMS INBOX</span></div>
      <section className="auth-card">
        <div className="auth-icon"><LockKeyhole size={25} strokeWidth={1.5} /></div>
        <span className="eyebrow">A QUIET PLACE FOR YOUR MESSAGES</span>
        <h1>Welcome to your inbox.</h1>
        <p>Your phone receives it. Relay keeps it in view.</p>
        {!session ? <div className="session-loading" role="status">{sessionError ? <><span>{sessionError}</span><button onClick={checkSession} className="primary-button">Try again</button></> : <><RefreshCw size={17} className="spin" /> Connecting to your inbox…</>}</div> : session.provider === "cloudflare" ?
          <div className="session-loading"><p>Sign in through the Cloudflare Access address for this inbox.</p><button onClick={() => window.location.reload()} className="primary-button">Reload inbox</button></div> :
          <form onSubmit={login}>
            <label htmlFor="password">Inbox password</label>
            <input id="password" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} placeholder="Enter your password" autoFocus />
            {sessionError && <p className="form-error" role="alert">{sessionError}</p>}
            <button className="primary-button" disabled={signingIn}>{signingIn ? "Signing in…" : "Open inbox"}<ArrowUpRight size={17} /></button>
          </form>}
        <div className="auth-foot"><ShieldCheck size={14} /> Private access · Incoming SMS</div>
      </section>
      <span className="auth-bottom">LESS NOISE. MORE SIGNAL.</span>
    </main>
  );

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <Brand />
        <div className="workspace-label"><span className="tiny-square" /> PERSONAL WORKSPACE</div>
        <nav aria-label="Main navigation"><button className="nav-item active" onClick={() => { setSearch(""); setSim(""); setPageCount(1); setMobileDetail(false); }}><Inbox size={18} /><span>Inbox</span><span className="nav-count">{number(stats.total)}</span></button></nav>
        <div className="sidebar-section"><span className="eyebrow">AT A GLANCE</span><div className="stat-row"><span>Messages in view</span><strong>{number(stats.total)}</strong></div><div className="stat-row"><span>Received today <small>UTC</small></span><strong>{number(stats.today)}</strong></div><div className="stat-row"><span>Connected SIMs</span><strong>{stats.sims.length.toString().padStart(2, "0")}</strong></div></div>
        <div className="sidebar-bottom">
          <div className="quiet-card"><div className="quiet-icon"><Radio size={20} strokeWidth={1.4} /></div><h3>Your phone, in sync.</h3><p>Incoming messages appear here automatically.</p><span><span className={`status-dot ${connection === "live" ? "" : "offline"}`} /> {connection === "live" ? "Listening for messages" : "Waiting for connection"}</span></div>
          <div className="readonly"><ShieldCheck size={15} /><span>Incoming messages only</span></div>
          {session.passwordRequired && <button className="sign-out" onClick={logout}><LogOut size={14} /> Sign out</button>}
          <div className="sidebar-footer"><span>RELAY / SMS INBOX</span><span>V.01</span></div>
        </div>
      </aside>

      <main className="workspace">
        <header className="topbar"><div className="breadcrumb"><span>Workspace</span><span>/</span><strong>Inbox</strong></div><div className="topbar-actions"><div className={`connection-pill ${connection !== "live" ? "is-reconnecting" : ""}`} role="status"><span className="status-dot" />{connection === "live" ? "Live updates on" : connection === "connecting" ? "Connecting…" : "Reconnecting…"}</div>{session.admin && <button className="icon-button manage-sims-button" aria-label="Manage administrators" onClick={() => setManagingAdmins(true)}><Users size={16} /><span>Administrators</span></button>}{session.passwordRequired && <button className="mobile-sign-out icon-button" aria-label="Sign out" onClick={logout}><LogOut size={14} /></button>}</div></header>
        <div className="page-heading"><div><div className="eyebrow heading-eyebrow">YOUR MESSAGES, TOGETHER</div><h1>Inbox<span className="heading-dot">.</span></h1><p>A little less checking your phone.</p></div><div className="heading-note"><Signal size={18} /><div><strong>Always in the loop</strong><span>New messages arrive in real time</span></div></div></div>

        <section className="inbox-panel" aria-label="SMS messages">
          <div className="inbox-toolbar"><div className="inbox-title"><Inbox size={17} /><strong>Conversations</strong><span className="count-badge">{number(stats.conversations)}</span></div><div className="toolbar-controls"><label className="sim-select"><Smartphone size={14} /><select aria-label="Filter by SIM" value={sim} onChange={(e) => { setSim(e.target.value); setPageCount(1); }}><option value="">All SIMs</option>{sims.map((value) => <option key={value.id} value={value.id}>{value.alias ? `${value.alias} (${value.id})` : value.id}</option>)}</select><ChevronDown size={13} /></label>{(session.provider !== "cloudflare" || session.admin) && <button className="icon-button manage-sims-button" aria-label="Manage SIMs" onClick={() => setManagingSIMs(true)}><Settings2 size={15} /><span>Manage SIMs</span></button>}<button className={`icon-button ${loading ? "is-loading" : ""}`} aria-label="Refresh inbox" onClick={() => setRevision((r) => r + 1)} disabled={loading}><RefreshCw size={15} className={loading ? "spin" : ""} /></button></div></div>
          {error && <div className="error-banner" role="alert"><span>{error}</span><button onClick={() => setRevision((r) => r + 1)}>Retry</button></div>}
          <div className={`inbox-columns ${mobileDetail ? "detail-open" : ""}`}>
            <div className="message-list-pane">
              <div className="search-wrap"><Search size={17} /><input aria-label="Search messages" placeholder="Search sender or message…" value={search} onChange={(e) => setSearch(e.target.value)} />{search && <button className="clear-search" aria-label="Clear search" onClick={() => setSearch("")}><X size={14} /></button>}</div>
              <div className="list-label"><span>{term || sim ? "FILTERED CHATS" : "LATEST CHATS"}</span><span>NEWEST FIRST <ArrowDown size={10} /></span></div>
              <div className="message-list" aria-busy={loading} ref={messageList}>
                {conversations.length === 0 && <div className="list-empty"><div className="empty-icon">{loading ? <RefreshCw size={23} className="spin" /> : <Inbox size={25} strokeWidth={1.3} />}</div><h3>{loading ? "Gathering your messages…" : term || sim ? "No matches here" : "A fresh start."}</h3><p>{loading ? "Just a moment." : term || sim ? "Try a different search or SIM." : "Your first forwarded SMS will appear here."}</p>{(term || sim) && !loading && <button className="text-button" onClick={() => { setSearch(""); setSim(""); setPageCount(1); }}>Clear filters <ArrowUpRight size={13} /></button>}</div>}
                {conversations.map(({ latestMessage: message, messageCount }) => <button key={message.from} className={`message-row ${selected === message.from ? "selected" : ""}`} onClick={(event) => { openedRow.current = event.currentTarget; listScrollTop.current = messageList.current?.scrollTop || 0; setSelected(message.from); setMobileDetail(true); }} aria-pressed={selected === message.from}><div className="row-top"><span className={`avatar avatar-${message.from.charCodeAt(0) % 4}`}>{initials(message.from)}</span><div className="row-identity"><strong>{message.from}</strong><span>{dateLabel(message.createdAt)}</span></div>{selected === message.from && <span className="selected-dot" />}</div><p dir="auto">{message.text}</p><div className="row-bottom"><span className="sim-badge"><MessageSquare size={10} />{number(messageCount)} {messageCount === 1 ? "message" : "messages"}</span><span className="received-label"><ArrowDown size={10} /> Received</span></div></button>)}
                {hasNext && <button className="load-more" disabled={loading} onClick={() => setPageCount((p) => p + 1)}>{loading ? "Loading…" : "Load more conversations"}<ArrowDown size={13} /></button>}
              </div>
              <div className="list-footer"><span>{number(conversations.length)} of {number(stats.conversations)} conversations</span><CheckCheck size={14} /></div>
            </div>

            <article className={`message-detail ${selected ? "conversation-detail" : ""}`} aria-label="Conversation" ref={messageDetail}>
              {selected ? <ConversationView key={`${selected}\0${sim}`} sender={selected} sim={sim} sims={sims} revision={revision} isOpen={mobileDetail} onBack={() => setMobileDetail(false)} onUnauthorized={sessionExpired} /> : <div className="detail-placeholder"><div className="placeholder-art"><span className="art-orbit orbit-one" /><span className="art-orbit orbit-two" /><MessageSquare size={37} strokeWidth={1} /><span className="art-spark">+</span></div><span className="eyebrow">A LITTLE SIGNAL. A LITTLE SIMPLER.</span><h2>{term || sim ? "Room for the right message." : "Your messages have a home."}</h2><p>{term || sim ? "Choose another filter to find what you’re looking for." : "Connect SMS Forwarder to your inbox. Every incoming message will find its way here."}</p><div className="placeholder-status"><span className={`status-dot ${connection === "live" ? "" : "offline"}`} />{connection === "live" ? "Ready when your phone is" : "Connecting to your inbox"}</div></div>}
            </article>
          </div>
        </section>
        <footer className="workspace-footer"><span><ShieldCheck size={12} /> Your messages. Just for you.</span><span aria-live="polite">{lastSync ? `Last synced ${lastSync.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}` : "Waiting for first sync"}<span className="footer-dot">·</span>Auto-updating</span></footer>
      </main>
      {managingSIMs && <SIMManager sims={sims} onClose={() => setManagingSIMs(false)} onChanged={simsChanged} onUnauthorized={sessionExpired} />}
      {managingAdmins && session.admin && session.email && <AdminManager currentEmail={session.email} onClose={() => setManagingAdmins(false)} onUnauthorized={sessionExpired} />}
    </div>
  );
}
