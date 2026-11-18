import {
  CloudAlert,
  CloudCheck,
  CloudOff,
  Cpu,
  FolderTree,
  Hourglass,
  LogOut,
  Monitor,
  Plug,
  Sparkles,
  SquareTerminal,
  TriangleAlert,
} from "lucide-react";
import { NavLink } from "react-router";
import { useSystem } from "../hooks/useSystem";
import { useNow } from "../hooks/useResource";
import { formatAgo, formatDateTime, formatMemory, parseTime, vmProblem } from "../lib/format";
import { syncState } from "../lib/sync";
import { VmPower } from "./VmPower";
import { IconButton, cx } from "./ui";

const tabs = [
  { to: "/studio", label: "Studio", icon: SquareTerminal },
  { to: "/desktop", label: "Desktop", icon: Monitor },
  { to: "/files", label: "Files", icon: FolderTree },
  { to: "/models", label: "Models", icon: Sparkles },
  { to: "/connectors", label: "Connectors", icon: Plug },
];

function SyncIndicator() {
  const { status, fetchedAt, error, outage } = useSystem();
  const now = useNow(1000);
  if (!status || outage) {
    // The last sync time is not current while the status poll fails.
    return null;
  }
  const storage = status.storage;
  const sync = syncState(storage);
  if (sync.kind === "none") {
    return null;
  }
  if (sync.kind === "unmounted") {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-warning">
        <CloudOff aria-hidden className="size-4" />
        Storage not mounted
      </span>
    );
  }
  if (sync.kind === "failed") {
    return (
      <span className="tooltip tooltip-left inline-flex items-center gap-1.5 text-xs text-error" data-tip={sync.error}>
        <CloudAlert aria-hidden className="size-4" />
        Sync failed
        <span className="sr-only">: {sync.error}</span>
      </span>
    );
  }
  const synced = sync.kind === "synced" ? parseTime(sync.at) : null;
  const serverNow = parseTime(status.time);
  const label =
    synced === null || serverNow === null
      ? "Not synced yet"
      : `Synced ${formatAgo(serverNow - synced + (now - fetchedAt))}`;
  return (
    <span
      className={cx("tooltip tooltip-left inline-flex items-center gap-1.5 text-xs tabular-nums", error ? "text-warning" : "text-base-content/70")}
      data-tip={`Last S3 sync: ${formatDateTime(storage.lastSyncAt)}. Last upload: ${formatDateTime(storage.lastUploadAt)}.`}
    >
      <CloudCheck aria-hidden className="size-4" />
      {label}
    </span>
  );
}

function VmResources() {
  const { vmStatus, status, outage } = useSystem();
  if (outage) {
    // Not current while the status poll fails.
    return null;
  }
  const driver = vmStatus?.driver ?? status?.vm.driver;
  return (
    <span className="hidden items-center gap-1.5 text-xs text-base-content/70 tabular-nums xl:inline-flex">
      <Cpu aria-hidden className="size-4" />
      {vmStatus && vmStatus.cpus > 0 ? (
        <span>
          {vmStatus.cpus} vCPU · {formatMemory(vmStatus.memMb)}
          {vmStatus.gpu ? " · GPU" : ""}
        </span>
      ) : null}
      {driver ? <span className="badge badge-soft badge-xs font-mono text-base-content/70">{driver}</span> : null}
    </span>
  );
}

/** Why the VM cannot run (for example a refused provider key); hidden while it runs. */
function VmProblem() {
  const { vmStatus, vmState, outage } = useSystem();
  const message = outage ? null : vmProblem(vmStatus, vmState);
  if (!message) {
    return null;
  }
  return (
    <span role="status" className="tooltip tooltip-left inline-flex max-w-xs min-w-0 items-center gap-1.5 text-xs text-error" data-tip={message}>
      <TriangleAlert aria-hidden className="size-4 shrink-0" />
      <span className="truncate">{message}</span>
    </span>
  );
}

/** A start or resume that takes long: the provider is slow, LydiaBox is not stuck. */
function VmSlow() {
  const { slowStart } = useSystem();
  if (!slowStart) {
    return null;
  }
  return (
    <span
      role="status"
      className="tooltip tooltip-left inline-flex max-w-xs min-w-0 items-center gap-1.5 text-xs text-warning"
      data-tip="The VM provider is slow to start or resume the VM. This can take a few minutes."
    >
      <Hourglass aria-hidden className="size-4 shrink-0" />
      <span className="truncate">The VM provider is slow.</span>
    </span>
  );
}

export function StatusBar({ email, onLogout }: { email?: string; onLogout: () => void }) {
  const { error, status, outage } = useSystem();
  return (
    // The bar keeps one row while everything fits; then the VM group wraps
    // as a whole to a second row, so every tab stays in view. Below 640 px
    // the tabs fill the first row, each as wide as its label needs.
    <header className="navbar min-h-0 shrink-0 flex-wrap gap-x-4 gap-y-2 border-b border-base-content/10 bg-base-200 px-3 py-2 sm:px-4">
      <div className="flex w-full min-w-0 items-center gap-2 sm:w-auto sm:shrink-0 sm:gap-4">
        <span className="flex shrink-0 items-center gap-2 text-sm font-semibold tracking-tight">
          <img src="/favicon.svg" alt="LydiaBox" className="size-5" />
          <span className="hidden sm:inline" aria-hidden>
            LydiaBox
          </span>
        </span>
        <nav aria-label="Workspace" className="-my-1 min-w-0 flex-1 sm:flex-none">
          <ul className="menu menu-horizontal w-full flex-nowrap gap-0.5 p-0 sm:gap-1">
            {tabs.map(({ to, label, icon: Icon }) => (
              <li key={to} className="flex-auto sm:flex-none">
                <NavLink
                  to={to}
                  className={({ isActive }) =>
                    cx(
                      "flex flex-col items-center justify-center gap-0.5 px-1 py-1 text-[11px] leading-tight font-medium whitespace-nowrap",
                      "sm:h-8 sm:flex-row sm:gap-1.5 sm:px-2.5 sm:py-0 sm:text-[13px]",
                      !isActive && "text-base-content/70 hover:text-base-content",
                    )
                  }
                >
                  <Icon aria-hidden className="size-4 shrink-0 sm:hidden lg:block" />
                  {label}
                </NavLink>
              </li>
            ))}
          </ul>
        </nav>
      </div>
      <div className="ml-auto flex min-w-0 flex-wrap items-center justify-end gap-x-4 gap-y-2">
        {error && !status && !outage ? (
          <span role="status" className="inline-flex items-center gap-1.5 text-xs text-error">
            <CloudOff aria-hidden className="size-4" />
            Server unreachable
          </span>
        ) : null}
        <SyncIndicator />
        <VmProblem />
        <VmSlow />
        <VmResources />
        <VmPower compact />
        {email ? <span className="hidden text-xs text-base-content/70 md:inline">{email}</span> : null}
        <IconButton icon={LogOut} label={email ? `Sign out ${email}` : "Sign out"} onClick={onLogout} />
      </div>
    </header>
  );
}
