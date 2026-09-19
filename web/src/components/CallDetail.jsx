import { useState, useEffect } from "react";
import { Link, useParams } from "react-router-dom";
import { formatBytes, formatDuration, formatTime, prettyJSON } from "../format.js";

function extractError(outputJSON) {
  try {
    const obj = JSON.parse(outputJSON);
    if (obj && typeof obj.error === "string") return obj.error;
  } catch (err) {
    // fall through
  }
  return outputJSON || "(no error detail)";
}

export default function CallDetail({ role }) {
  const { id: callId } = useParams();
  const [call, setCall] = useState(null);
  const [actor, setActor] = useState("");
  const [error, setError] = useState(null);

  useEffect(() => {
    let cancelled = false;
    fetch("/api/tool-calls/" + encodeURIComponent(callId))
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

  const statusClass = call ? (call.is_error ? "error" : "ok") : "";
  const statusLabel = call ? (call.is_error ? "error" : "ok") : "";

  return (
    <main>
      <Link className="back-link" to="/tool-calls">
        ← Back to history
      </Link>
      {error && <div className="callout">{error}</div>}
      {!call && !error && <div className="empty-state">Loading…</div>}
      {call && (
        <>
          <h2 className="section-title">
            <span className="tool-badge">{call.tool}</span>{" "}
            <span className={"status-badge " + statusClass}>{statusLabel}</span>
          </h2>

          <div className="panel">
            <h3>Call</h3>
            <dl className="kv">
              <dt>Actor</dt>
              <dd>
                <Link to={"/tool-calls?actor=" + encodeURIComponent(call.actor_id)}>{actor}</Link>
              </dd>
              <dt>When</dt>
              <dd>{formatTime(call.called_at)}</dd>
              <dt>Duration</dt>
              <dd>{formatDuration(call.duration_ms)}</dd>
              <dt>Response size</dt>
              <dd>{!call.is_error ? formatBytes(call.output_size) : "—"}</dd>
              <dt>Call ID</dt>
              <dd>{call.id}</dd>
            </dl>
          </div>

          <div className="panel">
            <h3>Arguments</h3>
            <pre className="payload">{prettyJSON(call.input_json)}</pre>
          </div>

          {call.is_error ? (
            <div className="panel">
              <h3>Error</h3>
              <p className="error-text">{extractError(call.output_json)}</p>
            </div>
          ) : (
            <div className="panel">
              <h3>Response</h3>
              <pre className="payload">{prettyJSON(call.output_json)}</pre>
              {call.truncated && (
                <p className="truncation-note">
                  Preview only — the full response was {formatBytes(call.output_size)}. Raise
                  <code> --history-preview-bytes</code> to keep more of it.
                </p>
              )}
            </div>
          )}
        </>
      )}
    </main>
  );
}
