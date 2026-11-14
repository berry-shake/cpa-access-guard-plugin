import { Routes, Route, Navigate, Link, useLocation } from "react-router-dom";
import { useEffect, useState } from "react";
import { isAuthed, subscribe, getSession, bootstrapFromPanel } from "./store/session";
import { useT } from "./i18n";
import Login from "./pages/Login";
import Mapping from "./pages/Mapping";
import Footer from "./components/Footer";

function useAuthTick() {
  const [, setTick] = useState(0);
  useEffect(() => subscribe(() => setTick((t) => t + 1)), []);
  return isAuthed();
}

// One navigation serves desktop, mobile, and the embedded plugin page.
export function TopNav() {
  const t = useT();
  const loc = useLocation();
  const s = getSession();
  if (!s) return null;
  // Active state: highlight the nav item matching the current path prefix.
  const onNativeKeys = loc.pathname === "/native-keys" || loc.pathname.startsWith("/native-keys/");
  const onPricing = loc.pathname === "/pricing" || loc.pathname.startsWith("/pricing/");
  return (
    <div className="topnav">
      <div className="topnav-inner">
        <div className="topnav-brand">
          <span className="tn-logo" aria-hidden="true">
            {/* Same artwork as internal/plugin/web/logo.svg; ids are prefixed to stay unique in the page. */}
            <svg viewBox="0 0 256 256" width="100%" height="100%">
              <defs>
                <linearGradient id="tn-logo-bg" x1="0" y1="0" x2="1" y2="1">
                  <stop offset="0" stopColor="#FF7B92" />
                  <stop offset="1" stopColor="#D92E5B" />
                </linearGradient>
                <mask id="tn-logo-ticket">
                  <rect x="24" y="64" width="208" height="128" rx="22" fill="#fff" />
                  <circle cx="24" cy="128" r="20" fill="#000" />
                  <circle cx="232" cy="128" r="20" fill="#000" />
                </mask>
              </defs>
              <rect width="256" height="256" rx="70" fill="url(#tn-logo-bg)" />
              <g transform="translate(128 128) scale(0.92) translate(-128 -128)">
                <rect x="24" y="64" width="208" height="128" rx="22" fill="#fff" mask="url(#tn-logo-ticket)" />
                <path d="M182 86 V170" stroke="#F4A0B2" strokeWidth="9" strokeLinecap="round" strokeDasharray="1 17" />
                <g fill="none" stroke="#D92E5B" strokeWidth="17" strokeLinecap="round" strokeLinejoin="round">
                  <circle cx="82" cy="128" r="23" />
                  <path d="M105 128 H160 M143 128 V152" />
                </g>
                <circle cx="207" cy="128" r="10" fill="#D92E5B" />
              </g>
            </svg>
          </span>
          <span className="tn-text">
            <span className="tn-title">{t("header.brand")}</span>
            <span className="tn-sub">{t("header.tagline")}</span>
          </span>
        </div>
        <nav className="topnav-actions" aria-label={t("header.navigation")}>
          <Link to="/native-keys" className={"tn-link" + (onNativeKeys ? " active" : "")} aria-current={onNativeKeys ? "page" : undefined}>{t("header.nativeKeys")}</Link>
          <Link to="/pricing" className={"tn-link" + (onPricing ? " active" : "")} aria-current={onPricing ? "page" : undefined}>{t("header.modelPricing")}</Link>
        </nav>
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
        <Route path="/native-keys" element={<Mapping section="native" />} />
        <Route path="/pricing" element={<Mapping section="pricing" />} />
        <Route path="*" element={<Navigate to="/native-keys" replace />} />
      </Routes>
      <Footer />
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
