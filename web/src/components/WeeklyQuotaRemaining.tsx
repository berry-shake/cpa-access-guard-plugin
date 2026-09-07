import { getLocale, useT } from "../i18n";
import type { NativeBindingUsageSummary } from "../types";

function formatUSD(value: number): string {
  return value > 0 && value < 0.01 ? "<$0.01" : `$${value.toFixed(2)}`;
}

export default function WeeklyQuotaRemaining({ usage }: { usage: NativeBindingUsageSummary }) {
  const t = useT();
  const limit = usage.weekly_usd_limit;
  const used = usage.weekly_usd_used;
  if (!Number.isFinite(limit) || limit <= 0 || !Number.isFinite(used) || used < 0) return null;

  const remaining = Math.max(0, limit - used);
  // Normalize floating-point noise before applying display and color thresholds.
  const percent = Number(((remaining / limit) * 100).toPrecision(12));
  const wholePercent = remaining < limit ? Math.min(99, Math.floor(percent)) : 100;
  const percentLabel = percent > 0 && percent < 1 ? "<1%" : `${wholePercent}%`;
  const level = percent < 5 ? "danger" : percent <= 20 ? "warn" : "ok";
  const amountLabel = t("mapping.native.weeklyRemainingAmount", {
    remaining: formatUSD(remaining),
    limit: formatUSD(limit),
  });

  // An idle, expired window gets a provisional reset date on every read.
  // Only show a date once usage has actually started the new window.
  const started = usage.weekly_calls > 0;
  const reset = started && usage.weekly_reset_at ? new Date(usage.weekly_reset_at) : null;
  const validReset = reset && Number.isFinite(reset.getTime()) ? reset : null;
  const pad = (value: number) => String(value).padStart(2, "0");
  const resetLabel = validReset
    ? `${pad(validReset.getMonth() + 1)}/${pad(validReset.getDate())} ${pad(validReset.getHours())}:${pad(validReset.getMinutes())}`
    : "";

  return (
    <div className={`native-weekly-quota ${level}`}>
      <div className="native-weekly-quota-heading">
        <span>{t("mapping.native.weeklyRemaining")}</span>
        {remaining === 0 && <span className="native-weekly-quota-exhausted">{t("mapping.native.quotaExhausted")}</span>}
      </div>
      <div className="native-weekly-quota-row">
        <span className="native-weekly-quota-badge">7D</span>
        <div
          className="native-weekly-quota-track"
          role="meter"
          aria-label={t("mapping.native.weeklyRemaining")}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          aria-valuetext={`${percentLabel} · ${amountLabel}`}
        >
          <div className="native-weekly-quota-fill" style={{ width: `${percent}%` }} />
        </div>
        <span className="native-weekly-quota-percent">{percentLabel}</span>
      </div>
      <div className="native-weekly-quota-detail">
        <span>{amountLabel}</span>
        {!started && <span>{t("mapping.native.quotaStartsOnUse")}</span>}
        {validReset && (
          <span>
            {t("mapping.native.quotaResetsAt")}{" "}
            <time dateTime={usage.weekly_reset_at} title={validReset.toLocaleString(getLocale(), { timeZoneName: "short" })}>
              {resetLabel}
            </time>
          </span>
        )}
      </div>
    </div>
  );
}
