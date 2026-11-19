import { useMemo } from "react";
import type { Model } from "../api/types";
import { Combobox } from "./Combobox";

interface ModelInputProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  models: Model[];
  /** Accessible name of the list, the same as the field label. */
  label: string;
  describedBy?: string;
}

/** The model ID when the gateway lists it, else "" (a stored binding can name a removed provider). */
export function listedModel(id: string | undefined, models: Model[]): string {
  if (!id) {
    return "";
  }
  return models.some((m) => m.id === id) ? id : "";
}

/** A model ID field that offers the listed `provider/model` IDs by provider and accepts any value. */
export function ModelInput({ id, value, onChange, models, label, describedBy }: ModelInputProps) {
  const options = useMemo(() => models.map((m) => ({ value: m.id, label: m.name || m.id, group: m.provider })), [models]);
  return <Combobox id={id} value={value} onChange={onChange} options={options} label={label} describedBy={describedBy} />;
}
