import { useT } from "../i18n";

// Same footer as the other berry-shake plugin pages (Wokey, Autoban, ...).
export default function Footer() {
  const t = useT();
  return (
    <footer className="app-footer">
      <span>{t("footer.text")}</span>{" · "}
      <a href="https://github.com/berry-shake/cpa-access-guard-plugin" target="_blank" rel="noopener noreferrer">{t("footer.source")}</a>
    </footer>
  );
}
