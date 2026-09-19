import { useState, useEffect } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { formatDuration, formatTime } from "../format.js";

// summarizeInput renders a call's stored input JSON as one compact line.
function summarizeInput(raw) {
  try {
    const args = JSON.parse(raw);
    if (args && typeof args === "object") {
      const parts = Object.entries(args)
        .filter(([, value]) => value !== "" && value !== 0 && value !== null)
        .map(([key, value]) => `${key}=${typeof value === "string" ? value : JSON.stringify(value)}`);
      if (parts.length > 0) return parts.join("  ");
    }
  } catch (err) {
    // Fall through to the raw string below.
  }
  return raw || "—";
}

export default function History({ role }) {
  const [calls, setCalls] = useState([]);
  const [actors, setActors] = useState({});
  const [nextCursor, setNextCursor] = useState("");
  const [tools, setTools] = useState([]);
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(true);

  const [searchParams, setSearchParams] = useSearchParams();
  const tool = searchParams.get("tool") || "";
  const isError = searchParams.get("is_error") || "";  // "true", "false", or ""
  const actor = searchParams.get("actor") || "";

  function buildURL(cursor) {
    const params = new URLSearchParams();
    if (tool) params.set("tool", tool);
    if (isError) params.set("is_error", isError);
    if (actor) params.set("actor", actor);
    if (cursor) params.set("cursor", cursor);
    return "/api/tool-calls?" + params.toString();
  }

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);

    fetch(buildURL(""))
      .then((res) => {
        if (!res.ok) throw new Error("request failed: " + res.status);
        return res.json();
      })
      .then((data) => {
        if (cancelled) return;
        setCalls(data.calls || []);
        setActors(data.actors || {});
        setNextCursor(data.next_cursor || "");
      })
      .catch((err) => !cancelled && setError(String(err)))
      .finally(() => !cancelled && setLoading(false));

    return () => {
      cancelled = true;
    };
  }, [tool, isError, actor]);

  useEffect(() => {
    fetch("/api/tool-calls/tools")
      .then((res) => res.json())
      .then((data) => setTools(data || []))
      .catch(() => setTools([]));
  }, []);

  function loadMore() {
    fetch(buildURL(nextCursor))
      .then((res) => res.json())
      .then((data) => {
        setCalls((prev) => prev.concat(data.calls || []));
        setActors((prev) => ({ ...prev, ...(data.actors || {}) }));
        setNextCursor(data.next_cursor || "");
      })
      .catch((err) => setError(String(err)));
  }

  function setFilter(key, value) {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      if (value) {
        next.set(key, value);
      } else {
        next.delete(key);
      }
      return next;
    });
  }

  return (
    <main>
      <div className="page-header">
        <h2 className="section-title">Tool call history</h2>
        <span className="count">{calls.length} calls shown</span>
      </div>
      {error && <div className="callout">Couldn't reach the server: {error}</div>}

      <div className="filter-bar">
        <select value={tool} onChange={(e) => setFilter("tool", e.target.value)} aria-label="Filter by tool">
          <option value="">All tools</option>
          {tools.map((name) => (
            <option key={name} value={name}>
              {name}
            </option>
          ))}
        </select>
        <select value={isError} onChange={(e) => setFilter("is_error", e.target.value)} aria-label="Filter by outcome">
          <option value="">Any outcome</option>
          <option value="false">ok</option>
          <option value="true">error</option>
        </select>
        <span className="grow"></span>
        {actor && (
          <button type="button" onClick={() => setFilter("actor", "")}>
            Actor: {actors[actor] || actor} ✕
          </button>
        )}
      </div>

      {loading ? (
        <div className="empty-state">Loading…</div>
      ) : calls.length === 0 ? (
        <div className="empty-state">
          No calls recorded yet. Point an agent at <code>/mcp</code> and its calls will show up here.
        </div>
      ) : (
        <>
          <div className="call-list">
            {calls.map((call) => {
              const statusClass = call.is_error ? "error" : "ok";
              const statusLabel = call.is_error ? "error" : "ok";
              return (
                <div className={"call-row " + statusClass} key={call.id}>
                  <span className={"status-badge " + statusClass}>{statusLabel}</span>
                  <span className="tool-badge">{call.tool}</span>
                  <Link className="args" to={"/tool-calls/" + call.id} title={call.input_json}>
                    {summarizeInput(call.input_json)}
                  </Link>
                  <Link
                    className="metric"
                    to={"/tool-calls?actor=" + encodeURIComponent(call.actor_id)}
                    title="Filter by this actor"
                  >
                    {actors[call.actor_id] || call.actor_id}
                  </Link>
                  <span className="metric">{formatDuration(call.duration_ms)}</span>
                  <time dateTime={call.called_at}>{formatTime(call.called_at)}</time>
                </div>
              );
            })}
          </div>
          {nextCursor && (
            <button type="button" className="load-more" onClick={loadMore}>
              Load more
            </button>
          )}
        </>
      )}
    </main>
  );
}
