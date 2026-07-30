import TopNav from "./TopNav.jsx";
import { navigate } from "./router.js";
import { formatDuration, formatTime } from "./format.js";

// summarizeArgs renders a call's stored arguments JSON as one compact line.
// The whole point of the list is scanning what agents actually asked for, so
// a bare "{...}" would make every row useless; the full payload is one click
// away in the detail view.
function summarizeArgs(raw) {
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

export default function HistoryList({ role, query }) {
  const [calls, setCalls] = React.useState([]);
  const [actors, setActors] = React.useState({});
  const [nextCursor, setNextCursor] = React.useState("");
  const [tools, setTools] = React.useState([]);
  const [error, setError] = React.useState(null);
  const [loading, setLoading] = React.useState(true);

  const tool = query.tool || "";
  const status = query.status || "";
  const actor = query.actor || "";

  function buildURL(cursor) {
    const params = new URLSearchParams();
    if (tool) params.set("tool", tool);
    if (status) params.set("status", status);
    if (actor) params.set("actor", actor);
    if (cursor) params.set("cursor", cursor);
    return "/api/history?" + params.toString();
  }

  // Refetches from scratch whenever a filter changes. buildURL is recreated
  // every render, so the filter values — not the function — are the deps.
  React.useEffect(() => {
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
  }, [tool, status, actor]);

  React.useEffect(() => {
    fetch("/api/history/tools")
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
    const next = { tool, status, actor };
    if (value) {
      next[key] = value;
    } else {
      delete next[key];
    }
    Object.keys(next).forEach((k) => !next[k] && delete next[k]);
    navigate("/history", next);
  }

  return (
    <>
      <header className="site">
        <h1>go-ai-webtools</h1>
        <TopNav active="history" role={role} />
        <span className="spacer"></span>
        <span className="count">{calls.length} calls shown</span>
      </header>
      <main>
        <h2 className="section-title">Tool call history</h2>
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
          <select value={status} onChange={(e) => setFilter("status", e.target.value)} aria-label="Filter by status">
            <option value="">Any outcome</option>
            <option value="ok">ok</option>
            <option value="error">error</option>
          </select>
          <span className="grow"></span>
          {actor && (
            <button type="button" onClick={() => setFilter("actor", "")}>
              Agent: {actors[actor] || actor} ✕
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
              {calls.map((call) => (
                <div className={"call-row " + call.status} key={call.id}>
                  <span className={"status-badge " + call.status}>{call.status}</span>
                  <span className="tool-badge">{call.tool}</span>
                  <a className="args" href={"#/history/" + call.id} title={call.args}>
                    {summarizeArgs(call.args)}
                  </a>
                  <a
                    className="metric"
                    href={"#/history?actor=" + encodeURIComponent(call.actor_id)}
                    title="Filter by this agent"
                  >
                    {actors[call.actor_id] || call.actor_id}
                  </a>
                  <span className="metric">{formatDuration(call.duration_ms)}</span>
                  <time dateTime={call.created_at}>{formatTime(call.created_at)}</time>
                </div>
              ))}
            </div>
            {nextCursor && (
              <button type="button" className="load-more" onClick={loadMore}>
                Load more
              </button>
            )}
          </>
        )}
      </main>
    </>
  );
}
