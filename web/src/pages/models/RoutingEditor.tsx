import { ArrowDown, ArrowUp, Plus, Save, Trash2, Undo2, X } from "lucide-react";
import { useEffect, useReducer, useState } from "react";
import { describeError } from "../../api/errors";
import { models as modelsApi } from "../../api/domains";
import type { Model, RoutingGroup } from "../../api/types";
import { StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { ModelInput } from "../../components/ModelInput";
import { Banner, Button, IconButton, Panel, Select, TextInput } from "../../components/ui";
import { emptyRouting, isDirty, routingReducer, validateGroups } from "./routing";

interface RoutingEditorProps {
  groups: RoutingGroup[];
  models: Model[];
  onSaved: (groups: RoutingGroup[]) => void;
}

function TargetAdder({ index, models, onAdd }: { index: number; models: Model[]; onAdd: (target: string) => void }) {
  const [value, setValue] = useState("");
  const id = `route-${index}-add`;
  return (
    <form
      className="flex gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        onAdd(value);
        setValue("");
      }}
    >
      <label htmlFor={id} className="sr-only">
        Model to add
      </label>
      <div className="min-w-0 flex-1">
        <ModelInput id={id} value={value} onChange={setValue} models={models} />
      </div>
      <Button type="submit" icon={Plus} disabled={!value.trim()}>
        Add
      </Button>
    </form>
  );
}

export function RoutingEditor({ groups, models, onSaved }: RoutingEditorProps) {
  const toast = useToast();
  const [state, dispatch] = useReducer(routingReducer, emptyRouting);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showErrors, setShowErrors] = useState(false);

  useEffect(() => {
    dispatch({ type: "load", groups });
  }, [groups]);

  const errors = validateGroups(state.groups);
  const dirty = isDirty(state);

  const save = async () => {
    setShowErrors(true);
    if (errors.size > 0) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const res = await modelsApi.saveRouting(state.groups.map((g) => ({ ...g, name: g.name.trim() })));
      dispatch({ type: "load", groups: res.groups });
      onSaved(res.groups);
      setShowErrors(false);
      toast.success("Routing groups saved");
    } catch (err) {
      setError(describeError(err, "Routing groups not saved"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Panel
      title="Routing groups"
      actions={
        <>
          {dirty ? (
            <Button size="sm" variant="ghost" icon={Undo2} onClick={() => dispatch({ type: "revert" })}>
              Revert
            </Button>
          ) : null}
          <Button size="sm" icon={Plus} onClick={() => dispatch({ type: "add" })}>
            Add group
          </Button>
          <Button size="sm" variant="primary" icon={Save} busy={busy} disabled={!dirty} onClick={() => void save()}>
            Save groups
          </Button>
        </>
      }
    >
      {error ? (
        <div className="px-4 pt-3">
          <Banner tone="danger">{error}</Banner>
        </div>
      ) : null}
      {state.groups.length === 0 ? (
        <StateView title="No routing groups" className="min-h-32">
          A group sends requests to several models, in order (fallback) or in turn (round-robin).
        </StateView>
      ) : (
        <ul className="flex flex-col divide-y divide-border-subtle">
          {state.groups.map((group, index) => {
            const problem = showErrors ? errors.get(index) : undefined;
            const nameId = `route-${index}-name`;
            return (
              <li key={index} className="flex flex-col gap-3 px-4 py-4">
                <div className="flex flex-wrap items-end gap-3">
                  <div className="flex min-w-40 flex-1 flex-col gap-1.5">
                    <label htmlFor={nameId} className="text-[13px] font-medium">
                      Name
                    </label>
                    <TextInput
                      id={nameId}
                      value={group.name}
                      spellCheck={false}
                      aria-invalid={problem ? true : undefined}
                      aria-describedby={problem ? `${nameId}-error` : undefined}
                      onChange={(e) => dispatch({ type: "rename", index, name: e.target.value })}
                    />
                  </div>
                  <div className="flex w-40 flex-col gap-1.5">
                    <label htmlFor={`route-${index}-strategy`} className="text-[13px] font-medium">
                      Strategy
                    </label>
                    <Select
                      id={`route-${index}-strategy`}
                      value={group.strategy}
                      onChange={(e) =>
                        dispatch({ type: "strategy", index, strategy: e.target.value === "round-robin" ? "round-robin" : "fallback" })
                      }
                    >
                      <option value="fallback">Fallback</option>
                      <option value="round-robin">Round-robin</option>
                    </Select>
                  </div>
                  <IconButton icon={Trash2} tone="danger" size="md" label={`Remove group ${group.name || index + 1}`} onClick={() => dispatch({ type: "remove", index })} />
                </div>
                {problem ? (
                  <p id={`${nameId}-error`} role="alert" className="text-xs text-danger-fg">
                    {problem}
                  </p>
                ) : null}
                {group.targets.length > 0 ? (
                  <ol className="flex flex-col gap-1">
                    {group.targets.map((target, t) => (
                      <li key={target} className="flex items-center gap-2 rounded-md bg-surface px-2 py-1">
                        <span className="w-5 text-right text-xs text-fg-muted tabular-nums">{t + 1}</span>
                        <code className="min-w-0 flex-1 truncate text-[13px]" title={target}>
                          {target}
                        </code>
                        <IconButton icon={ArrowUp} label={`Move ${target} up`} disabled={t === 0} onClick={() => dispatch({ type: "moveTarget", index, from: t, to: t - 1 })} />
                        <IconButton
                          icon={ArrowDown}
                          label={`Move ${target} down`}
                          disabled={t === group.targets.length - 1}
                          onClick={() => dispatch({ type: "moveTarget", index, from: t, to: t + 1 })}
                        />
                        <IconButton icon={X} tone="danger" label={`Remove ${target}`} onClick={() => dispatch({ type: "removeTarget", index, target: t })} />
                      </li>
                    ))}
                  </ol>
                ) : null}
                <TargetAdder index={index} models={models} onAdd={(target) => dispatch({ type: "addTarget", index, target })} />
              </li>
            );
          })}
        </ul>
      )}
    </Panel>
  );
}
