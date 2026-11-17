import { CircleAlert, CloudOff, PackageX, PowerOff, RotateCw } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { availabilityOf, describeError } from "../api/errors";
import { statusOutage } from "../lib/outage";
import { OutageView } from "./Outage";
import { Button, Spinner, cx } from "./ui";

interface StateViewProps {
  icon?: LucideIcon;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}

/** A centered message for empty, error, and unavailable states. */
export function StateView({ icon: Icon, title, children, action, className }: StateViewProps) {
  return (
    <div className={cx("flex h-full min-h-48 flex-col items-center justify-center gap-3 px-6 py-10 text-center", className)}>
      {Icon ? <Icon aria-hidden className="size-6 text-fg-muted" /> : null}
      <p className="text-sm font-medium text-fg text-balance">{title}</p>
      {children ? <div className="max-w-md text-sm text-fg-muted text-pretty">{children}</div> : null}
      {action ? <div className="mt-1 flex flex-wrap justify-center gap-2">{action}</div> : null}
    </div>
  );
}

export function LoadingView({ label, className }: { label: string; className?: string }) {
  return (
    <div role="status" className={cx("flex h-full min-h-32 items-center justify-center gap-2 text-sm text-fg-muted", className)}>
      <Spinner />
      {label}
    </div>
  );
}

interface ErrorViewProps {
  error: unknown;
  /** The subsystem, as people name it: "Sessions", "The model gateway". */
  subsystem: string;
  /** In-context failure sentence: "Sessions not loaded". */
  failed: string;
  onRetry?: () => void;
  /** Extra action for the unavailable case, such as "Start". */
  unavailableAction?: ReactNode;
  className?: string;
}

/**
 * Renders a load failure. Missing routes (404), unbuilt subsystems (501), and
 * stopped dependencies (503) each get their own message and recovery path.
 */
export function ErrorView({ error, subsystem, failed, onRetry, unavailableAction, className }: ErrorViewProps) {
  const retry = onRetry ? (
    <Button icon={RotateCw} onClick={onRetry}>
      Retry
    </Button>
  ) : null;
  // The cell is stopped or starts (LydiaBox Cloud): the VM state is the
  // title, not "<subsystem> is not running".
  const cell = statusOutage(error, false);
  if (cell) {
    return <OutageView outage={cell} />;
  }
  switch (availabilityOf(error)) {
    case "missing":
    case "not_implemented":
      return (
        <StateView icon={PackageX} title={`${subsystem} is not available in this build`} className={className} action={retry}>
          Update LydiaBox to a release that includes it.
        </StateView>
      );
    case "unavailable":
      return (
        <StateView
          icon={PowerOff}
          title={`${subsystem} is not available`}
          className={className}
          action={
            <>
              {unavailableAction}
              {retry}
            </>
          }
        >
          {describeError(error, failed).replace(`${failed}. `, "")}
        </StateView>
      );
    default:
      return (
        <StateView icon={error instanceof TypeError ? CloudOff : CircleAlert} title={failed} className={className} action={retry}>
          {describeError(error, failed).replace(`${failed}. `, "")}
        </StateView>
      );
  }
}
