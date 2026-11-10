import { ArrowRightLeft, FileSearch } from "lucide-react";
import { useEffect, useState } from "react";
import { describeError } from "../../api/errors";
import { sessions } from "../../api/domains";
import { HARNESSES } from "../../api/types";
import type { AgentHarness, Model, Session, SwitchPreview, SwitchResult } from "../../api/types";
import { Dialog } from "../../components/Dialog";
import { ModelInput } from "../../components/ModelInput";
import { Banner, Button, Field, Select } from "../../components/ui";
import { count } from "../../lib/format";
import { harnessLabels, sessionName } from "../../lib/harness";

interface SwitchDialogProps {
  open: boolean;
  onClose: () => void;
  source: Session | null;
  models: Model[];
  onSwitched: (result: SwitchResult) => void;
}

function ListBlock({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) {
    return null;
  }
  return (
    <div className="flex flex-col gap-1">
      <h4 className="text-xs font-semibold text-fg-muted">{title}</h4>
      <ul className="flex flex-col gap-0.5 font-mono text-xs text-fg">
        {items.map((item, i) => (
          <li key={`${i}-${item}`} className="break-all">
            {item}
          </li>
        ))}
      </ul>
    </div>
  );
}

function PreviewView({ preview }: { preview: SwitchPreview }) {
  const { state } = preview;
  return (
    <div className="flex flex-col gap-4">
      {state.originalGoal ? (
        <div className="flex flex-col gap-1">
          <h4 className="text-xs font-semibold text-fg-muted">Original goal</h4>
          <p className="text-sm text-pretty text-fg">{state.originalGoal}</p>
        </div>
      ) : null}
      <div className="grid gap-4 sm:grid-cols-2">
        <ListBlock title={count(state.modifiedFiles.length, { one: "# modified file", other: "# modified files" })} items={state.modifiedFiles} />
        <ListBlock title="Unresolved issues" items={state.unresolvedIssues} />
      </div>
      {state.gitDiffStat.trim() ? (
        <p className="font-mono text-xs text-fg-muted">{state.gitDiffStat.trim()}</p>
      ) : null}
      <div className="flex flex-col gap-1">
        <h4 className="text-xs font-semibold text-fg-muted">.agent/HANDOFF.md</h4>
        <pre className="max-h-80 overflow-auto rounded-md border border-border-subtle bg-bg p-3 text-xs leading-relaxed whitespace-pre-wrap text-fg">
          {preview.handoffMarkdown}
        </pre>
      </div>
    </div>
  );
}

export function SwitchDialog({ open, onClose, source, models, onSwitched }: SwitchDialogProps) {
  const targets = HARNESSES.filter((h) => h !== source?.harness);
  const [target, setTarget] = useState<AgentHarness>(targets[0] ?? "dsh");
  const [model, setModel] = useState("");
  const [preview, setPreview] = useState<SwitchPreview | null>(null);
  const [busy, setBusy] = useState<"preview" | "switch" | null>(null);
  const [error, setError] = useState<string | null>(null);

  const sourceHarness = source?.harness;
  useEffect(() => {
    if (open) {
      setTarget(HARNESSES.find((h) => h !== sourceHarness) ?? "dsh");
      setModel("");
      setPreview(null);
      setError(null);
    }
  }, [open, sourceHarness]);

  if (!source) {
    return null;
  }

  const request = () => ({ sourceSessionId: source.id, targetHarness: target, targetModel: model.trim() || undefined });

  const runPreview = async () => {
    setBusy("preview");
    setError(null);
    try {
      setPreview(await sessions.preview(request()));
    } catch (err) {
      setError(describeError(err, "Handoff preview not loaded"));
    } finally {
      setBusy(null);
    }
  };

  const runSwitch = async () => {
    setBusy("switch");
    setError(null);
    try {
      const result = await sessions.switch(request());
      onSwitched(result);
      onClose();
    } catch (err) {
      setError(describeError(err, "Harness not switched"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={`Switch ${sessionName(source)}`}
      width="lg"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button icon={FileSearch} busy={busy === "preview"} disabled={busy !== null} onClick={() => void runPreview()}>
            {preview ? "Refresh preview" : "Preview handoff"}
          </Button>
          <Button
            variant="primary"
            icon={ArrowRightLeft}
            busy={busy === "switch"}
            disabled={busy !== null || !preview}
            title={preview ? undefined : "Preview the handoff first"}
            onClick={() => void runSwitch()}
          >
            Switch to {harnessLabels[target]}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field id="sw-harness" label="Target harness">
            <Select
              id="sw-harness"
              value={target}
              onChange={(e) => {
                setTarget(targets.find((h) => h === e.target.value) ?? target);
                setPreview(null);
              }}
            >
              {targets.map((h) => (
                <option key={h} value={h}>
                  {harnessLabels[h]}
                </option>
              ))}
            </Select>
          </Field>
          <Field id="sw-model" label="Model" hint="Empty keeps the target's bound model.">
            <ModelInput
              id="sw-model"
              value={model}
              onChange={(v) => {
                setModel(v);
                setPreview(null);
              }}
              models={models}
              describedBy="sw-model-hint"
            />
          </Field>
        </div>
        {error ? <Banner tone="danger">{error}</Banner> : null}
        {preview ? (
          <PreviewView preview={preview} />
        ) : (
          <p className="text-sm text-fg-muted text-pretty">
            The switch interrupts {sessionName(source)}, writes .agent/HANDOFF.md in {source.workspaceDir}, and starts{" "}
            {harnessLabels[target]} in the same terminal.
          </p>
        )}
      </div>
    </Dialog>
  );
}
