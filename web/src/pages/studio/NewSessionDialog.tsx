import { Play } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { models as modelsApi, sessions } from "../../api/domains";
import { SESSION_HARNESSES } from "../../api/types";
import type { HarnessID, Model, Session } from "../../api/types";
import { Dialog } from "../../components/Dialog";
import { Combobox } from "../../components/Combobox";
import { ModelInput, listedModel } from "../../components/ModelInput";
import { Banner, Button, Field, Select, TextInput } from "../../components/ui";
import { engineFor, harnessLabels } from "../../lib/harness";
import { NoModelNotice } from "./NoModelNotice";
import type { ModelProblem } from "./NoModelNotice";

interface NewSessionDialogProps {
  open: boolean;
  onClose: () => void;
  onCreated: (session: Session) => void;
  workspaces: string[];
  models: Model[];
  /** Why an agent cannot reach a model, or null. */
  noModel: ModelProblem | null;
  size: () => { cols: number; rows: number };
  defaultHarness: HarnessID | "";
}

const LAST_HARNESS = "lydiabox.newSession.harness";

/** The harness of the last session started in this browser, or null. */
function lastHarness(): HarnessID | null {
  try {
    const v = localStorage.getItem(LAST_HARNESS);
    return SESSION_HARNESSES.find((h) => h === v) ?? null;
  } catch {
    return null;
  }
}

function rememberHarness(h: HarnessID) {
  try {
    localStorage.setItem(LAST_HARNESS, h);
  } catch {
    // Storage is blocked: the dialog starts with the default next time.
  }
}

function validWorkspace(dir: string): boolean {
  const clean = dir.trim();
  const inside = (root: string) => clean === root || clean.startsWith(`${root}/`);
  return (inside("/workspace") || inside("/home/agent")) && !clean.split("/").includes("..");
}

export function NewSessionDialog({ open, onClose, onCreated, workspaces: listed, models, noModel, size, defaultHarness }: NewSessionDialogProps) {
  // Only folders that the dialog accepts are offered (a list cached by an
  // older host can hold a guest link target such as /data/workspace).
  const workspaces = useMemo(() => listed.filter(validWorkspace), [listed]);
  const [harness, setHarness] = useState<HarnessID>("claude-code");
  const [workspace, setWorkspace] = useState("/workspace");
  // null until the user types: the field then shows the last model of the harness.
  const [typedModel, setModel] = useState<string | null>(null);
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [workspaceError, setWorkspaceError] = useState<string | null>(null);
  // The last model of each harness (the server keeps it at each start).
  const [lastModels, setLastModels] = useState<Partial<Record<HarnessID, string>>>({});

  // The last model shows only while the gateway lists it.
  const model = typedModel ?? listedModel(lastModels[harness], models);
  const workspaceOptions = useMemo(() => workspaces.map((w) => ({ value: w })), [workspaces]);

  const chooseHarness = (h: HarnessID) => {
    setHarness(h);
    setModel(null);
  };

  useEffect(() => {
    if (!open) {
      return;
    }
    const controller = new AbortController();
    modelsApi
      .bindings(controller.signal)
      .then(({ bindings }) => {
        const byHarness: Partial<Record<HarnessID, string>> = {};
        for (const b of bindings) {
          if (b.model) {
            byHarness[b.harness] = b.model;
          }
        }
        setLastModels(byHarness);
      })
      .catch(() => {
        // Without the last models the field starts empty.
      });
    return () => controller.abort();
  }, [open]);

  useEffect(() => {
    if (open) {
      setHarness(lastHarness() ?? (defaultHarness || "claude-code"));
      setWorkspace(workspaces[0] ?? "/workspace");
      setModel(null);
      setTitle("");
      setError(null);
      setWorkspaceError(null);
    }
  }, [open, defaultHarness, workspaces]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!validWorkspace(workspace)) {
      setWorkspaceError("Choose a folder inside /workspace or /home/agent.");
      return;
    }
    setWorkspaceError(null);
    setBusy(true);
    setError(null);
    try {
      const { cols, rows } = size();
      const created = await sessions.create({
        harness,
        workspaceDir: workspace.trim().replace(/\/+$/, ""),
        model: model.trim() || undefined,
        title: title.trim() || undefined,
        engine: engineFor(harness, "chat"),
        cols,
        rows,
      });
      rememberHarness(harness);
      onCreated(created);
      onClose();
    } catch (err) {
      setError(describeError(err, "Session not started"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title="New session"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="new-session" variant="primary" icon={Play} busy={busy}>
            {busy ? "Starting…" : "Start session"}
          </Button>
        </>
      }
    >
      <form id="new-session" onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        {error ? <Banner tone="danger">{error}</Banner> : null}
        {noModel && harness !== "shell" ? <NoModelNotice problem={noModel} onNavigate={onClose} /> : null}
        <Field id="ns-harness" label="Harness">
          <Select
            id="ns-harness"
            value={harness}
            onChange={(e) => chooseHarness(SESSION_HARNESSES.find((h) => h === e.target.value) ?? "claude-code")}
          >
            {SESSION_HARNESSES.map((h) => (
              <option key={h} value={h}>
                {harnessLabels[h]}
              </option>
            ))}
          </Select>
        </Field>
        <Field id="ns-workspace" label="Workspace folder" error={workspaceError}>
          <Combobox
            id="ns-workspace"
            label="Workspace folder"
            value={workspace}
            onChange={setWorkspace}
            options={workspaceOptions}
            invalid={Boolean(workspaceError)}
            describedBy={workspaceError ? "ns-workspace-error" : undefined}
          />
        </Field>
        {harness !== "shell" ? (
          <Field id="ns-model" label="Model">
            <ModelInput id="ns-model" label="Model" value={model} onChange={setModel} models={models} />
          </Field>
        ) : null}
        <Field id="ns-title" label="Title">
          <TextInput id="ns-title" value={title} onChange={(e) => setTitle(e.target.value)} />
        </Field>
      </form>
    </Dialog>
  );
}
