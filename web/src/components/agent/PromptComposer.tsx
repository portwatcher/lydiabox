import { CircleStop, SendHorizontal } from "lucide-react";
import { forwardRef, useState } from "react";
import { Button, cx, fieldClass } from "../ui";
import { DictationButton } from "./DictationButton";

export interface PromptComposerProps {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  /** Shows Stop: the agent works. */
  working: boolean;
  onInterrupt: () => void;
  sending?: boolean;
  disabled?: boolean;
  placeholder?: string;
}

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

/** The prompt field: Cmd/Ctrl+Enter sends, Enter adds a line. Stop interrupts the agent while it works. */
export const PromptComposer = forwardRef<HTMLTextAreaElement, PromptComposerProps>(function PromptComposer(
  { value, onChange, onSubmit, working, onInterrupt, sending = false, disabled = false, placeholder },
  ref,
) {
  const [interim, setInterim] = useState("");
  const canSend = value.trim() !== "" && !disabled && !sending;
  const shortcut = isMac ? "⌘↵" : "Ctrl+↵";
  return (
    <form
      className="flex flex-col gap-2 rounded-box border border-base-content/20 bg-base-100 p-2 focus-within:border-base-content/45"
      onSubmit={(e) => {
        e.preventDefault();
        if (canSend) {
          onSubmit();
        }
      }}
    >
      <textarea
        ref={ref}
        value={interim ? `${value}${value && !value.endsWith(" ") ? " " : ""}${interim}` : value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            if (canSend) {
              onSubmit();
            }
          }
        }}
        rows={2}
        disabled={disabled}
        placeholder={placeholder}
        aria-label="Message to the agent"
        aria-keyshortcuts="Control+Enter Meta+Enter"
        className={cx("textarea field-sizing-content max-h-60 min-h-16 w-full resize-none border-none bg-transparent px-2 text-sm focus:outline-none", fieldClass)}
      />
      <div className="flex items-center gap-1">
        <DictationButton
          disabled={disabled}
          onInterim={setInterim}
          onFinal={(text) => onChange(`${value}${value && !value.endsWith(" ") ? " " : ""}${text.trim()}`)}
        />
        <span className="flex-1" />
        {working ? (
          <Button size="sm" variant="ghost" icon={CircleStop} onClick={onInterrupt} disabled={disabled}>
            Stop
          </Button>
        ) : null}
        <Button size="sm" variant="primary" type="submit" icon={SendHorizontal} busy={sending} disabled={!canSend}>
          Send <kbd className="hidden text-[11px] opacity-80 sm:inline">{shortcut}</kbd>
        </Button>
      </div>
    </form>
  );
});
