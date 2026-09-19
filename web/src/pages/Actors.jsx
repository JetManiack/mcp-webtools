import { useState, useEffect } from "react";
import { Link } from "react-router-dom";
import { formatTime } from "../format.js";

export default function Actors({ role }) {
  const [agents, setAgents] = useState([]);
  const [displayName, setDisplayName] = useState("");
  const [issuedToken, setIssuedToken] = useState(null);
  const [credsByAgent, setCredsByAgent] = useState({});
  const [error, setError] = useState(null);

  function loadAgents() {
    setError(null);
    fetch("/api/actors")
      .then((res) => {
        if (!res.ok) throw new Error("request failed: " + res.status);
        return res.json();
      })
      .then(setAgents)
      .catch((err) => setError(String(err)));
  }

  useEffect(loadAgents, []);

  function loadCredentials(agentID) {
    fetch(`/api/actors/${agentID}/credentials`)
      .then((res) => res.json())
      .then((creds) => setCredsByAgent((prev) => ({ ...prev, [agentID]: creds })))
      .catch((err) => setError(String(err)));
  }

  function toggleCredentials(agentID) {
    if (credsByAgent[agentID]) {
      setCredsByAgent((prev) => {
        const next = { ...prev };
        delete next[agentID];
        return next;
      });
      return;
    }
    loadCredentials(agentID);
  }

  function handleCreate(e) {
    e.preventDefault();
    fetch("/api/actors", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ display_name: displayName }),
    })
      .then(async (res) => {
        if (!res.ok) {
          const body = await res.json().catch(() => ({}));
          throw new Error(body.error || "request failed: " + res.status);
        }
        setDisplayName("");
        loadAgents();
      })
      .catch((err) => setError(String(err)));
  }

  function handleIssueToken(agentID) {
    fetch(`/api/actors/${agentID}/credentials`, { method: "POST" })
      .then((res) => res.json())
      .then((data) => {
        setIssuedToken(data.token);
        loadAgents();
        if (credsByAgent[agentID]) loadCredentials(agentID);
      })
      .catch((err) => setError(String(err)));
  }

  function handleRevokeToken(credID) {
    fetch(`/api/credentials/${credID}`, { method: "DELETE" })
      .then(() => {
        loadAgents();
        for (const agentID of Object.keys(credsByAgent)) {
          loadCredentials(agentID);
        }
      })
      .catch((err) => setError(String(err)));
  }

  function handleRevokeAll(agentID) {
    fetch(`/api/actors/${agentID}`, { method: "DELETE" })
      .then(() => {
        loadAgents();
        if (credsByAgent[agentID]) loadCredentials(agentID);
      })
      .catch((err) => setError(String(err)));
  }

  const activeCount = agents.filter((agent) => agent.has_active_token).length;

  return (
    <main>
      <div className="page-header">
        <h2 className="section-title">Actors</h2>
        <span className="count">{activeCount} active / {agents.length} registered</span>
      </div>
      {error && <div className="callout">{error}</div>}
      {issuedToken && (
        <div className="transmission">
          <span className="label">New token</span>
          <code>{issuedToken}</code>
          <button type="button" onClick={() => setIssuedToken(null)}>
            Copied, dismiss
          </button>
        </div>
      )}
      <form className="dispatch-bar" onSubmit={handleCreate}>
        <input
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder="Name a new actor"
          aria-label="New actor name"
        />
        <button type="submit" className="primary">
          Register actor
        </button>
      </form>
      {agents.length === 0 ? (
        <div className="empty-state">No actors yet. Register one to issue its first token.</div>
      ) : (
        <div className="agent-grid">
          {agents.map((agent) => (
            <div className="agent-card" key={agent.id}>
              <div className="beacon-row">
                <span className={"beacon" + (agent.has_active_token ? " active" : "")}></span>
                <span className="name">{agent.display_name}</span>
                <span className={"status-label" + (agent.has_active_token ? " active" : "")}>
                  {agent.has_active_token ? "active" : "revoked"}
                </span>
              </div>
              <code className="agent-id">{agent.id}</code>
              <div className="actions">
                <button type="button" onClick={() => handleIssueToken(agent.id)}>
                  Issue token
                </button>
                <button type="button" onClick={() => toggleCredentials(agent.id)}>
                  {credsByAgent[agent.id] ? "Hide credentials" : "Credentials"}
                </button>
                <button type="button" onClick={() => handleRevokeAll(agent.id)}>
                  Revoke all
                </button>
                <Link
                  className="metric"
                  to={"/tool-calls?actor=" + encodeURIComponent(agent.id)}
                >
                  History
                </Link>
              </div>
              {credsByAgent[agent.id] && (
                <ul className="token-list">
                  {credsByAgent[agent.id].length === 0 && <li>No credentials issued.</li>}
                  {credsByAgent[agent.id].map((cred) => (
                    <li key={cred.id}>
                      <span className={cred.revoked_at ? "revoked" : ""}>
                        {cred.id.slice(0, 8)} · {cred.revoked_at ? "revoked" : "active"} ·{" "}
                        {cred.last_used_at ? "used " + formatTime(cred.last_used_at) : "never used"}
                      </span>
                      {!cred.revoked_at && (
                        <button type="button" onClick={() => handleRevokeToken(cred.id)}>
                          Revoke
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
          ))}
        </div>
      )}
    </main>
  );
}
