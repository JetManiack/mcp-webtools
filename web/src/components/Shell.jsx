import { Outlet, NavLink } from "react-router-dom";

export default function Shell({ role, pages }) {
  function handleLogout() {
    fetch("/auth/logout", { method: "POST" }).then(() => {
      window.location.href = "/";
    });
  }

  return (
    <div className="app-layout">
      <aside className="sidebar">
        <div className="sidebar-brand">go-ai-webtools</div>
        <nav className="sidebar-nav">
          <NavLink
            to="/tool-calls"
            className={({ isActive }) => "sidebar-link" + (isActive ? " active" : "")}
          >
            Tool Calls
          </NavLink>
          {role === "admin" && (
            <NavLink
              to="/actors"
              className={({ isActive }) => "sidebar-link" + (isActive ? " active" : "")}
            >
              Actors
            </NavLink>
          )}
          {pages.map((p) => (
            <NavLink
              key={p.path}
              to={p.path}
              className={({ isActive }) => "sidebar-link" + (isActive ? " active" : "")}
            >
              {p.label}
            </NavLink>
          ))}
        </nav>
        <button type="button" className="logout-link" onClick={handleLogout}>
          Log out
        </button>
      </aside>
      <div className="content-area">
        <Outlet />
      </div>
    </div>
  );
}
