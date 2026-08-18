import { Routes, Route, Navigate, useNavigate, Link, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { isAuthed, subscribe, clearSession, getSession, bootstrapFromPanel } from "./store/session";
import { useT } from "./i18n";
import Login from "./pages/Login";
import KeyList from "./pages/KeyList";
import KeyNew from "./pages/KeyNew";
import KeyEdit from "./pages/KeyEdit";
import KeyUsage from "./pages/KeyUsage";
import ModelPick from "./pages/ModelPick";
import Mapping, { AliasEditForm, RuleEditForm } from "./pages/Mapping";

function useAuthTick() {
  const [, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((t) => t + 1)), []);
  return isAuthed();
}

// Desktop top horizontal nav, styled after the Model Limiter page header:
// left = logo mark + product name + one-line tagline, right = nav links +
// logout as a plain text link. Mobile keeps the legacy .header (hidden on
// desktop via CSS) and bottom tab bar instead.
function TopNav() {
  const t = useT();
  const nav = useNavigate();
  const loc = useLocation();
  const s = getSession();
  if (!s) return null;
  // Active state: highlight the nav item matching the current path prefix.
  const onKeys = loc.pathname === "/keys" || loc.pathname.startsWith("/keys/");
  const onNew = loc.pathname === "/keys/new" || loc.pathname.startsWith("/keys/new/");
  const onMapping = loc.pathname === "/mapping" || loc.pathname.startsWith("/mapping/");
  return (
    <div className="topnav">
      <div className="topnav-inner">
        <div className="topnav-brand">
          <span className="tn-logo" aria-hidden="true">
            <svg viewBox="0 0 256 256" width="20" height="20">
              <path
                d="M128 58 L182 78 V130 C182 166 160 190 128 202 C96 190 74 166 74 130 V78 Z"
                fill="none" stroke="currentColor" strokeWidth="16" strokeLinejoin="round"
              />
              <circle cx="128" cy="120" r="12" fill="currentColor" />
              <path d="M128 126 V156" stroke="currentColor" strokeWidth="14" strokeLinecap="round" />
            </svg>
          </span>
          <span className="tn-text">
            <span className="tn-title">{t("header.brand")}</span>
            <span className="tn-sub">{t("header.tagline")}</span>
          </span>
        </div>
        <div className="topnav-actions">
          <Link to="/keys" className={"tn-link" + (onKeys && !onNew ? " active" : "")}>{t("header.keyList")}</Link>
          <Link to="/keys/new" className={"tn-link" + (onNew ? " active" : "")}>{t("header.newKey")}</Link>
          <Link to="/mapping" className={"tn-link" + (onMapping ? " active" : "")}>{t("header.mapping")}</Link>
          <button
            className="tn-link tn-logout"
            onClick={() => { clearSession(); nav("/login"); }}
          >
            {t("header.logout")}
          </button>
        </div>
      </div>
    </div>
  );
}

function Shell() {
  const authed = useAuthTick();
  const [bootstrapped, setBootstrapped] = useState(false);
  const t = useT();

  // When not yet authenticated, try once to reuse the panel's saved
  // management key (same-origin iframe embed). Only runs when not authed and
  // not already attempted, so a manual login or a successful bootstrap won't
  // re-trigger it.
  useEffect(() => {
    if (authed || bootstrapped) return;
    let alive = true;
    void bootstrapFromPanel().finally(() => {
      if (alive) setBootstrapped(true);
    });
    return () => {
      alive = false;
    };
  }, [authed, bootstrapped]);

  if (!authed) {
    if (!bootstrapped) {
      return <div className="app muted" style={{ padding: "40px 20px" }}>{t("session.restoring")}</div>;
    }
    return (
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route path="*" element={<Navigate to="/login" replace />} />
      </Routes>
    );
  }
  return (
    <div className="app">
      <TopNav />
      <Routes>
        <Route path="/keys" element={<KeyList />} />
        <Route path="/keys/new" element={<KeyNew />} />
        <Route path="/keys/new/models" element={<ModelPick />} />
        <Route path="/keys/:id/edit" element={<KeyEdit />} />
        <Route path="/keys/:id/edit/models" element={<ModelPick />} />
        <Route path="/mapping/pick-target" element={<ModelPick />} />
        <Route path="/keys/:id/usage" element={<KeyUsage />} />
        <Route path="/mapping" element={<Mapping />} />
        <Route path="/mapping/alias/:aliasName" element={<AliasEditForm />} />
        <Route path="/mapping/rule/:ruleName" element={<RuleEditForm />} />
        <Route path="*" element={<Navigate to="/keys" replace />} />
      </Routes>
    </div>
  );
}

export default function App() {
  return (
    <Routes>
      <Route path="/*" element={<Shell />} />
    </Routes>
  );
}
