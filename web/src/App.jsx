import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import Shell from "./components/Shell.jsx";
import History from "./pages/History.jsx";
import Actors from "./pages/Actors.jsx";
import CallDetail from "./components/CallDetail.jsx";
import { useCurrentUser } from "./currentUser.js";
import { domainPages } from "./pages/domain/index.js";

export default function App() {
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

  return (
    <BrowserRouter>
      <Routes>
        <Route element={<Shell role={user.role} pages={domainPages} />}>
          <Route path="/tool-calls" element={<History role={user.role} />} />
          <Route path="/tool-calls/:id" element={<CallDetail role={user.role} />} />
          {user.role === "admin" && (
            <Route path="/actors" element={<Actors role={user.role} />} />
          )}
          {domainPages.map((p) => (
            <Route key={p.path} path={p.path} element={<p.component role={user.role} />} />
          ))}
          <Route path="*" element={<Navigate to="/tool-calls" replace />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}
