import { useHashRoute } from "./router.js";
import { useCurrentUser } from "./currentUser.js";
import Agents from "./Agents.jsx";
import HistoryList from "./HistoryList.jsx";
import CallDetail from "./CallDetail.jsx";

export default function App() {
  const route = useHashRoute();
  const { user, error } = useCurrentUser();

  if (error) {
    return (
      <div className="login-gate">
        <p>You need to log in to view tool-call history.</p>
        <a href="/auth/login">Log in</a>
      </div>
    );
  }

  if (!user) {
    return <div className="empty-state">Loading…</div>;
  }

  if (route.path === "/agents") {
    // Viewers can't manage agents, and the tab isn't shown to them — a
    // hand-typed #/agents falls back to history rather than rendering a
    // screen whose every request would 403.
    if (user.role !== "admin") {
      return <HistoryList role={user.role} query={route.query} />;
    }
    return <Agents role={user.role} />;
  }

  const callMatch = route.path.match(/^\/history\/(.+)$/);
  if (callMatch) {
    return <CallDetail callId={callMatch[1]} role={user.role} />;
  }

  return <HistoryList role={user.role} query={route.query} />;
}
