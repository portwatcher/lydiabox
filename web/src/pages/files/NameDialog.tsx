import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { Dialog } from "../../components/Dialog";
import { Banner, Button, Field, TextInput, describedBy } from "../../components/ui";

interface NameDialogProps {
  open: boolean;
  title: string;
  label: string;
  submitLabel: string;
  initial: string;
  /** Failure sentence, such as "Folder not created". */
  failed: string;
  /** Returns an inline error for the value, or null when it is valid. */
  validate: (value: string) => string | null;
  onSubmit: (value: string) => Promise<void>;
  onClose: () => void;
}

/** One text field in a dialog, for new folder and rename. */
export function NameDialog({ open, title, label, submitLabel, initial, failed, validate, onSubmit, onClose }: NameDialogProps) {
  const [value, setValue] = useState(initial);
  const [fieldError, setFieldError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (open) {
      setValue(initial);
      setFieldError(null);
      setError(null);
    }
  }, [open, initial]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const invalid = validate(value.trim());
    setFieldError(invalid);
    if (invalid) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await onSubmit(value.trim());
      onClose();
    } catch (err) {
      setError(describeError(err, failed));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      width="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="name-dialog" variant="primary" busy={busy}>
            {submitLabel}
          </Button>
        </>
      }
    >
      <form id="name-dialog" onSubmit={(e) => void submit(e)} className="flex flex-col gap-3" noValidate>
        {error ? <Banner tone="danger">{error}</Banner> : null}
        <Field id="name-input" label={label} error={fieldError}>
          <TextInput
            id="name-input"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            autoFocus
            spellCheck={false}
            aria-invalid={fieldError ? true : undefined}
            aria-describedby={describedBy("name-input", false, Boolean(fieldError))}
          />
        </Field>
      </form>
    </Dialog>
  );
}
