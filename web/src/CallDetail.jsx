import TopNav from "./TopNav.jsx";
import { formatBytes, formatDuration, formatTime, prettyJSON } from "./format.js";

export default function CallDetail({ callId, role }) {
  const [call, setCall] = React.useState(null);
  const [actor, setActor] = React.useState("");
  const [error, setError] = React.useState(null);

  React.useEffect(() => {
    let cancelled = false;
    fetch("/api/history/" + encodeURIComponent(callId))
      .then((res) => {
        if (res.status === 404) throw new Error("no such call");
        if (!res.ok) throw new Error("request failed: " + res.status);
        return res.json();
      })
      .then((data) => {
        if (cancelled) return;
        setCall(data.call);
        setActor(data.actor || data.call.actor_id);
      })
      .catch((err) => !cancelled && setError(String(err)));
    return () => {
      cancelled = true;
    };
  }, [callId]);

  return (
    <>
      <header className="site">
        <h1>go-ai-webtools</h1>
        <TopNav active="history" role={role} />
      </header>
      <main>
        <a className="back-link" href="#/history">
          ← Back to history
        </a>
        {error && <div className="callout">{error}</div>}
        {!call && !error && <div className="empty-state">Loading…</div>}
        {call && (
          <>
            <h2 className="section-title">
              <span className="tool-badge">{call.tool}</span> <span className={"status-badge " + call.status}>{call.status}</span>
            </h2>

            <div className="panel">
              <h3>Call</h3>
              <dl className="kv">
                <dt>Agent</dt>
                <dd>
                  <a href={"#/history?actor=" + encodeURIComponent(call.actor_id)}>{actor}</a>
                </dd>
                <dt>When</dt>
                <dd>{formatTime(call.created_at)}</dd>
                <dt>Duration</dt>
                <dd>{formatDuration(call.duration_ms)}</dd>
                <dt>Response size</dt>
                <dd>{call.status === "ok" ? formatBytes(call.response_bytes) : "—"}</dd>
                <dt>Call ID</dt>
                <dd>{call.id}</dd>
              </dl>
            </div>

            <div className="panel">
              <h3>Arguments</h3>
              <pre className="payload">{prettyJSON(call.args)}</pre>
            </div>

            {call.status === "error" ? (
              <div className="panel">
                <h3>Error</h3>
                <p className="error-text">{call.error_message}</p>
              </div>
            ) : (
              <div className="panel">
                <h3>Response</h3>
                <pre className="payload">{prettyJSON(call.response_preview)}</pre>
                {call.truncated && (
                  <p className="truncation-note">
                    Preview only — the full response was {formatBytes(call.response_bytes)}. Raise
                    <code> --history-preview-bytes</code> to keep more of it.
                  </p>
                )}
              </div>
            )}
          </>
        )}
      </main>
    </>
  );
}
