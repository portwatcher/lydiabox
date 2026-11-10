import { useId } from "react";
import type { Model } from "../api/types";
import { TextInput } from "./ui";

interface ModelInputProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  models: Model[];
  placeholder?: string;
  describedBy?: string;
}

/** A model ID field that suggests known `provider/model` IDs and accepts any value. */
export function ModelInput({ id, value, onChange, models, placeholder = "kimi/kimi-k3", describedBy }: ModelInputProps) {
  const listId = useId();
  return (
    <>
      <TextInput
        id={id}
        list={listId}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        spellCheck={false}
        autoComplete="off"
        aria-describedby={describedBy}
      />
      <datalist id={listId}>
        {models.map((m) => (
          <option key={m.id} value={m.id} />
        ))}
      </datalist>
    </>
  );
}
