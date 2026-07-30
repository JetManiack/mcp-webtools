import TopNav from "./TopNav.jsx";
import { formatTime } from "./format.js";

export default function Agents({ role }) {
  const [agents, setAgents] = React.useState([]);
  const [displayName, setDisplayName] = React.useState("");
  const [issuedToken, setIssuedToken] = React.useState(null);
  const [tokensByAgent, setTokensByAgent] = React.useState({});
  const [error, setError] = React.useState(null);

  function loadAgents() {
    setError(null);
    fetch("/api/agents")
      .then((res) => {
        if (!res.ok) throw new Error("request failed: " + res.status);
        return res.json();
      })
      .then(setAgents)
      .catch((err) => setError(String(err)));
  }

  React.useEffect(loadAgents, []);

  function loadTokens(agentID) {
    fetch(`/api/agents/${agentID}/tokens`)
      .then((res) => res.json())
      .then((tokens) => setTokensByAgent((prev) => ({ ...prev, [agentID]: tokens })))
      .catch((err) => setError(String(err)));
  }

  function toggleTokens(agentID) {
    if (tokensByAgent[agentID]) {
      setTokensByAgent((prev) => {
        const next = { ...prev };
        delete next[agentID];
        return next;
      });
      return;
    }
    loadTokens(agentID);
  }

  function handleCreate(e) {
    e.preventDefault();
    fetch("/api/agents", {
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
    fetch(`/api/agents/${agentID}/tokens`, { method: "POST" })
      .then((res) => res.json())
      .then((data) => {
        setIssuedToken(data.token);
        loadAgents();
        if (tokensByAgent[agentID]) loadTokens(agentID);
      })
      .catch((err) => setError(String(err)));
  }

  function handleRevokeToken(agentID, tokenID) {
    fetch(`/api/agents/${agentID}/tokens/${tokenID}`, { method: "DELETE" })
      .then(() => {
        loadAgents();
        loadTokens(agentID);
      })
      .catch((err) => setError(String(err)));
  }

  function handleRevokeAll(agentID) {
    fetch(`/api/agents/${agentID}`, { method: "DELETE" })
      .then(() => {
        loadAgents();
        if (tokensByAgent[agentID]) loadTokens(agentID);
      })
      .catch((err) => setError(String(err)));
  }

  const activeCount = agents.filter((agent) => agent.has_active_token).length;

  return (
    <>
      <header className="site">
        <h1>go-ai-webtools</h1>
        <TopNav active="agents" role={role} />
        <span className="spacer"></span>
        <span className="count">
          {activeCount} active / {agents.length} registered
        </span>
      </header>
      <main>
        <h2 className="section-title">Agents</h2>
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
            placeholder="Name a new agent"
            aria-label="New agent name"
          />
          <button type="submit" className="primary">
            Register agent
          </button>
        </form>
        {agents.length === 0 ? (
          <div className="empty-state">No agents yet. Register one to issue its first token.</div>
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
                  <button type="button" onClick={() => toggleTokens(agent.id)}>
                    {tokensByAgent[agent.id] ? "Hide tokens" : "Tokens"}
                  </button>
                  <button type="button" onClick={() => handleRevokeAll(agent.id)}>
                    Revoke all
                  </button>
                  <a className="metric" href={"#/history?actor=" + encodeURIComponent(agent.id)}>
                    History
                  </a>
                </div>
                {tokensByAgent[agent.id] && (
                  <ul className="token-list">
                    {tokensByAgent[agent.id].length === 0 && <li>No tokens issued.</li>}
                    {tokensByAgent[agent.id].map((token) => (
                      <li key={token.id}>
                        <span className={token.revoked_at ? "revoked" : ""}>
                          {token.id.slice(0, 8)} · {token.revoked_at ? "revoked" : "active"} ·{" "}
                          {token.last_used_at ? "used " + formatTime(token.last_used_at) : "never used"}
                        </span>
                        {!token.revoked_at && (
                          <button type="button" onClick={() => handleRevokeToken(agent.id, token.id)}>
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
    </>
  );
}
