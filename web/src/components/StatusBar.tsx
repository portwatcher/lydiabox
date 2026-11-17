import {
  CloudAlert,
  CloudCheck,
  CloudOff,
  Cpu,
  FolderTree,
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
  const { status, fetchedAt, error } = useSystem();
  const now = useNow(1000);
  if (!status) {
    return null;
  }
  const storage = status.storage;
  const sync = syncState(storage);
  if (sync.kind === "none") {
    return null;
  }
  if (sync.kind === "unmounted") {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-warning-fg">
        <CloudOff aria-hidden className="size-4" />
        Storage not mounted
      </span>
    );
  }
  if (sync.kind === "failed") {
    return (
      <span className="inline-flex items-center gap-1.5 text-xs text-danger-fg" title={sync.error}>
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
      className={cx("inline-flex items-center gap-1.5 text-xs tabular-nums", error ? "text-warning-fg" : "text-fg-muted")}
      title={`Last S3 sync: ${formatDateTime(storage.lastSyncAt)}. Last upload: ${formatDateTime(storage.lastUploadAt)}.`}
    >
      <CloudCheck aria-hidden className="size-4" />
      {label}
    </span>
  );
}

function VmResources() {
  const { vmStatus, status } = useSystem();
  const driver = vmStatus?.driver ?? status?.vm.driver;
  return (
    <span className="hidden items-center gap-1.5 text-xs text-fg-muted tabular-nums xl:inline-flex">
      <Cpu aria-hidden className="size-4" />
      {vmStatus && vmStatus.cpus > 0 ? (
        <span>
          {vmStatus.cpus} vCPU · {formatMemory(vmStatus.memMb)}
          {vmStatus.gpu ? " · GPU" : ""}
        </span>
      ) : null}
      {driver ? <span className="rounded bg-surface-active px-1.5 font-mono text-[11px] text-fg-muted">{driver}</span> : null}
    </span>
  );
}

/** Why the VM cannot run (for example a refused provider key); hidden while it runs. */
function VmProblem() {
  const { vmStatus, vmState } = useSystem();
  const message = vmProblem(vmStatus, vmState);
  if (!message) {
    return null;
  }
  return (
    <span role="status" className="inline-flex max-w-xs min-w-0 items-center gap-1.5 text-xs text-danger-fg" title={message}>
      <TriangleAlert aria-hidden className="size-4 shrink-0" />
      <span className="truncate">{message}</span>
    </span>
  );
}

export function StatusBar({ email, onLogout }: { email?: string; onLogout: () => void }) {
  const { error, status } = useSystem();
  return (
    <header className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 border-b border-border-subtle bg-bg-subtle px-3 py-2 sm:flex-nowrap sm:px-4">
      <div className="flex w-full min-w-0 items-center gap-3 sm:w-auto sm:gap-4">
        <span className="flex shrink-0 items-center gap-2 text-sm font-semibold tracking-tight">
          <img src="/favicon.svg" alt="LydiaBox" className="size-5" />
          <span className="hidden sm:inline" aria-hidden>
            LydiaBox
          </span>
        </span>
        <nav aria-label="Workspace" className="-my-1 flex min-w-0 flex-1 gap-0.5 overflow-x-auto sm:gap-1">
          {tabs.map(({ to, label, icon: Icon }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cx(
                  "inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-[13px] font-medium whitespace-nowrap transition-colors duration-150 sm:px-2.5",
                  isActive ? "bg-surface-active text-fg" : "text-fg-muted hover:bg-surface-hover hover:text-fg",
                )
              }
            >
              <Icon aria-hidden className="hidden size-4 sm:block" />
              {label}
            </NavLink>
          ))}
        </nav>
      </div>
      <div className="ml-auto flex shrink-0 flex-wrap items-center gap-x-4 gap-y-2 sm:flex-nowrap">
        {error && !status ? (
          <span role="status" className="inline-flex items-center gap-1.5 text-xs text-danger-fg">
            <CloudOff aria-hidden className="size-4" />
            Server unreachable
          </span>
        ) : null}
        <SyncIndicator />
        <VmProblem />
        <VmResources />
        <VmPower compact />
        {email ? <span className="hidden text-xs text-fg-muted md:inline">{email}</span> : null}
        <IconButton icon={LogOut} label={email ? `Sign out ${email}` : "Sign out"} onClick={onLogout} />
      </div>
    </header>
  );
}
