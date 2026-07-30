export default function TopNav({ active, role }) {
  function handleLogout() {
    fetch("/auth/logout", { method: "POST" }).then(() => {
      window.location.href = "/";
    });
  }

  return (
    <nav className="nav-tabs">
      <a href="#/history" className={active === "history" ? "active" : ""}>
        History
      </a>
      {role === "admin" && (
        <a href="#/agents" className={active === "agents" ? "active" : ""}>
          Agents
        </a>
      )}
      <button type="button" className="logout-link" onClick={handleLogout}>
        Log out
      </button>
    </nav>
  );
}
