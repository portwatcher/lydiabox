import type { Model } from "../../api/types";
import { Select } from "../../components/ui";

interface SessionModelMenuProps {
  value: string;
  models: Model[];
  onChange: (model: string) => void;
  disabled?: boolean;
}

/** The model of a session. The harness of a session cannot change; its model can. */
export function SessionModelMenu({
  value,
  models,
  onChange,
  disabled,
}: SessionModelMenuProps) {
  const ids = models.map((m) => m.id);
  if (value && !ids.includes(value)) {
    ids.unshift(value);
  }
  return (
    <div className="w-40 shrink-0 sm:w-56">
      <Select
        aria-label="Model"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
      >
        {value ? null : <option value="">Default model</option>}
        {ids.map((id) => (
          <option key={id} value={id}>
            {id}
          </option>
        ))}
      </Select>
    </div>
  );
}
